package analyze

import (
	"math/rand"
	"reflect"
	"testing"
)

// The thresholds are published in docs/analyze.md; changing them changes every rebuild count.
func TestRebuildThresholdsArePinned(t *testing.T) {
	if RebuildFactor != 3 || RebuildMinTokens != 20_000 {
		t.Fatalf("rebuild thresholds changed (factor %d, min %d): update docs/analyze.md and the CHANGELOG", RebuildFactor, RebuildMinTokens)
	}
}

const sonnet = "claude-sonnet-5-5"
const opus5 = "claude-opus-5"

// req builds a request at minute m with context ctx split as cache read + write.
func lreq(m int64, read, write int64) LedgerRequest {
	return LedgerRequest{SessionKey: "c-1", TS: m * 60_000, Model: sonnet, Plan: "api_key", AgentVersion: "2.1.330",
		Effort: "medium", Input: 10, CacheRead: read, CacheWrite5m: write, Output: 100}
}

func lwith(r LedgerRequest, f func(*LedgerRequest)) LedgerRequest { f(&r); return r }

func TestRebuildLedgerCauses(t *testing.T) {
	base := lreq(0, 0, 30_000) // session start
	warm := lreq(1, 30_000, 2_000)
	cases := []struct {
		name string
		next LedgerRequest
		want string // "" = not a rebuild
	}{
		{"ordinary growth", lreq(2, 32_000, 3_000), ""},
		{"write exactly the minimum", lreq(2, 12_000, 20_000), ""},
		{"write exactly 3x new content", lwith(lreq(2, 0, 30_000), func(r *LedgerRequest) { r.Input = 32_010 + 10_000 - 30_000 }), ""}, // new content 10k, write 30k
		{"idle past the learned 5m TTL", lreq(8, 0, 35_000), CauseIdle},
		{"gap inside TTL, same everything", lreq(2, 0, 35_000), CauseUnexplained},
		{"model changed", lwith(lreq(2, 0, 35_000), func(r *LedgerRequest) { r.Model = "claude-haiku-5-5" }), CauseModel},
		{"idle wins over model change", lwith(lreq(9, 0, 35_000), func(r *LedgerRequest) { r.Model = "claude-haiku-5-5" }), CauseIdle},
		{"compaction flag", lwith(lreq(2, 0, 25_000), func(r *LedgerRequest) { r.Compacted = true }), CauseCompaction},
		{"compaction summary phase", lwith(lreq(2, 0, 25_000), func(r *LedgerRequest) { r.Phase = "compact" }), CauseCompaction},
		{"version changed", lwith(lreq(2, 0, 35_000), func(r *LedgerRequest) { r.AgentVersion = "2.1.331" }), CauseVersion},
		{"effort on a 5.5 model keeps the cache", lwith(lreq(2, 0, 35_000), func(r *LedgerRequest) { r.Effort = "high" }), CauseUnexplained},
		{"fast mode", lwith(lreq(2, 0, 35_000), func(r *LedgerRequest) { r.ServiceTier = "fast" }), CauseFastMode},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := RebuildLedger([]LedgerRequest{base, warm, c.next})
			if len(got) == 0 || got[0].Cause != CauseSessionStart {
				t.Fatalf("first main request should be a session start: %+v", got)
			}
			got = got[1:]
			if c.want == "" {
				if len(got) != 0 {
					t.Errorf("want no rebuild, got %+v", got)
				}
				return
			}
			if len(got) != 1 || got[0].Cause != c.want {
				t.Errorf("want %s, got %+v", c.want, got)
			}
		})
	}
}

func TestRebuildEffortOnModelWithPerEffortCache(t *testing.T) {
	a := lwith(lreq(0, 0, 30_000), func(r *LedgerRequest) { r.Model = opus5 })
	b := lwith(lreq(1, 0, 30_000), func(r *LedgerRequest) { r.Model = opus5; r.Effort = "high" })
	got := RebuildLedger([]LedgerRequest{a, b})
	if len(got) != 2 || got[1].Cause != CauseEffort {
		t.Errorf("got %+v", got)
	}
}

func TestRebuildSubagentAndTTL(t *testing.T) {
	sub := func(m, read, write int64) LedgerRequest {
		r := lreq(m, read, write)
		r.AgentKey, r.Sidechain = "a1", true
		r.CacheWrite5m, r.CacheWrite1h = 0, 0 // no write yet seen: TTL comes from the default
		r.CacheWriteNA = write
		return r
	}
	got := RebuildLedger([]LedgerRequest{sub(0, 0, 25_000), sub(6, 0, 25_000)})
	if len(got) != 2 || got[0].Cause != CauseSubagent || got[1].Cause != CauseIdle || !got[1].TTLEstimated || got[1].TTLMS != ttl5m {
		t.Errorf("subagent: %+v", got)
	}
	// Main conversation on a subscription: documented default 1h, so 30 minutes idle is not expiry.
	m := func(at int64) LedgerRequest {
		r := lreq(at, 0, 25_000)
		r.Plan, r.CacheWrite5m, r.CacheWriteNA = "subscription", 0, 25_000
		return r
	}
	got = RebuildLedger([]LedgerRequest{m(0), m(30), m(100)})
	if len(got) != 3 || got[1].Cause != CauseUnexplained || got[2].Cause != CauseIdle || got[2].TTLMS != ttl1h || !got[2].TTLEstimated {
		t.Errorf("subscription main: %+v", got)
	}
	// A 1h write seen in the log beats the default for an API key.
	h := func(at int64) LedgerRequest {
		r := lreq(at, 0, 25_000)
		r.CacheWrite5m, r.CacheWrite1h = 0, 25_000
		return r
	}
	got = RebuildLedger([]LedgerRequest{h(0), h(30)})
	if len(got) != 2 || got[1].Cause != CauseUnexplained || got[1].TTLEstimated || got[1].TTLMS != ttl1h {
		t.Errorf("learned 1h: %+v", got)
	}
}

func TestLedgersIgnoreInputOrder(t *testing.T) {
	reqs := []LedgerRequest{lreq(0, 0, 30_000), lreq(1, 30_000, 2_000), lreq(8, 0, 35_000), lreq(9, 35_000, 1_000)}
	reqs = append(reqs, lwith(lreq(3, 0, 40_000), func(r *LedgerRequest) { r.SessionKey = "c-2" }))
	want := RebuildLedger(reqs)
	wantOut, _ := OutputLedger(reqs)
	for i := 0; i < 20; i++ {
		s := append([]LedgerRequest(nil), reqs...)
		rand.New(rand.NewSource(int64(i))).Shuffle(len(s), func(a, b int) { s[a], s[b] = s[b], s[a] })
		if got := RebuildLedger(s); !reflect.DeepEqual(got, want) {
			t.Fatalf("rebuild ledger depends on input order")
		}
		if got, _ := OutputLedger(s); !reflect.DeepEqual(got, wantOut) {
			t.Fatalf("output ledger depends on input order")
		}
	}
}

func TestSummarizeRebuilds(t *testing.T) {
	es := []RebuildEntry{
		{TS: 5, Cause: CauseSessionStart, CacheWrite: 30_000},
		{TS: 10, Cause: CauseIdle, CacheWrite: 30_000, TTLEstimated: true},
		{TS: 11, Cause: CauseIdle, CacheWrite: 30_000},
		{TS: 12, Cause: CauseUnexplained, CacheWrite: 40_000},
		{TS: 99, Cause: CauseModel, CacheWrite: 50_000}, // outside the window
	}
	s := SummarizeRebuilds(es, 0, 50, 500_000)
	if s.RebuildCount != 3 || s.RebuildTokens != 100_000 || len(s.Rows) != 3 {
		t.Fatalf("%+v", s)
	}
	if r := s.Rows[1]; r.Cause != CauseIdle || r.Count != 2 || r.Tokens != 60_000 || r.Share != 0.6 || r.TTLGuess != 1 {
		t.Errorf("idle row: %+v", r)
	}
	if r := s.Rows[0]; r.Cause != CauseSessionStart || r.Share != 0 {
		t.Errorf("session starts are shown but not shared: %+v", r)
	}
}

func TestModelVersion(t *testing.T) {
	cases := []struct {
		in           string
		fam          string
		major, minor int
		ok           bool
	}{
		{"claude-opus-5-5", "opus", 5, 5, true},
		{"claude-fable-5-2-20261101", "fable", 5, 2, true},
		{"claude-opus-5", "opus", 5, 0, true},
		{"claude-sonnet-4-6[1m]", "sonnet", 4, 6, true},
		{"claude-opus-4-1-20250805", "opus", 4, 1, true},
		{"<synthetic>", "", 0, 0, false},
		{"gpt-5", "gpt", 5, 0, true},
	}
	for _, c := range cases {
		f, ma, mi, ok := modelVersion(c.in)
		if f != c.fam || ma != c.major || mi != c.minor || ok != c.ok {
			t.Errorf("%s: %s %d %d %v", c.in, f, ma, mi, ok)
		}
	}
	for m, want := range map[string]bool{"claude-opus-5-5": true, "claude-fable-5-1": true, "claude-fable-6-0": true, "claude-opus-5": false, "claude-sonnet-4-6": false} {
		if effortKeepsCache(m) != want {
			t.Errorf("effortKeepsCache(%s) != %v", m, want)
		}
	}
	for m, want := range map[string]bool{"claude-opus-5-5": true, "claude-mythos-5-2": true, "claude-fable-5-3": false, "claude-opus-5": false, "claude-opus-4-6": false} {
		if preservesThinking(m) != want {
			t.Errorf("preservesThinking(%s) != %v", m, want)
		}
	}
}

func lptr(n int64) *int64 { return &n }

func lout(m int64, output int64, thinking *int64, text, tool int64) LedgerRequest {
	r := lreq(m, 1_000, 100)
	r.Output, r.Thinking, r.TextBytes, r.ToolInputBytes, r.HasSizes = output, thinking, text, tool, true
	return r
}

func TestOutputSplit(t *testing.T) {
	cases := []struct {
		name                 string
		r                    LedgerRequest
		thinking, text, tool int64
		measured, unsplit    bool
	}{
		{"measured thinking, split by bytes", lout(0, 1000, lptr(600), 300, 100), 600, 300, 100, true, false},
		{"measured, no visible bytes", lout(0, 1000, lptr(1000), 0, 0), 1000, 0, 0, true, false},
		{"thinking above output is capped", lout(0, 100, lptr(150), 0, 0), 100, 0, 0, true, false},
		{"absent thinking: output minus visible at 3.5 bytes/token", lout(0, 1000, nil, 700, 0), 800, 200, 0, false, false},
		{"absent thinking, visible larger than output", lout(0, 100, nil, 7000, 0), 0, 100, 0, false, false},
		{"no sizes and no thinking", lwith(lout(0, 500, nil, 0, 0), func(r *LedgerRequest) { r.HasSizes = false }), 0, 0, 0, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			es, _ := OutputLedger([]LedgerRequest{c.r})
			e := es[0]
			if e.Thinking != c.thinking || e.Text != c.text || e.ToolInput != c.tool || e.ThinkingMeasured != c.measured || e.Unsplit != c.unsplit {
				t.Errorf("got %+v", e)
			}
			if !e.Unsplit && e.Thinking+e.Text+e.ToolInput != e.Output {
				t.Errorf("split does not add up to output: %+v", e)
			}
		})
	}
}

func TestOutputCalibration(t *testing.T) {
	var reqs []LedgerRequest
	for i := range minCalibrationRequests {
		reqs = append(reqs, lout(int64(i), 300, lptr(100), 400, 0)) // 400 bytes for 200 tokens: 2.0 bytes/token
	}
	reqs = append(reqs, lout(100, 1000, nil, 800, 0)) // visible = 800 / 2.0 = 400, so thinking 600
	es, ratios := OutputLedger(reqs)
	if len(ratios) != 1 || !ratios[0].Calibrated || ratios[0].Ratio != 2.0 || ratios[0].Requests != minCalibrationRequests {
		t.Fatalf("ratios: %+v", ratios)
	}
	if last := es[len(es)-1]; last.Thinking != 600 || last.ThinkingMeasured {
		t.Errorf("estimated with calibrated ratio: %+v", last)
	}
	_, ratios = OutputLedger(reqs[minCalibrationRequests-1:]) // one measured request is not enough
	if ratios[0].Calibrated || ratios[0].Ratio != DefaultBytesPerToken {
		t.Errorf("too few requests should use the prior: %+v", ratios)
	}
}

func TestThinkingCarried(t *testing.T) {
	a := lout(0, 500, lptr(100), 0, 0)
	b := lout(1, 500, lptr(200), 0, 0)
	c := lwith(lout(2, 500, lptr(50), 0, 0), func(r *LedgerRequest) { r.Compacted = true })
	d := lout(3, 500, lptr(10), 0, 0)
	e := lwith(lout(4, 500, lptr(10), 0, 0), func(r *LedgerRequest) { r.Model = opus5 })
	es, _ := OutputLedger([]LedgerRequest{a, b, c, d, e})
	want := []struct {
		carried int64
		counted bool
	}{{0, true}, {100, true}, {0, true}, {50, true}, {0, false}}
	for i, w := range want {
		if es[i].Carried != w.carried || es[i].CarryCounted != w.counted {
			t.Errorf("request %d: carried %d counted %v, want %d %v", i, es[i].Carried, es[i].CarryCounted, w.carried, w.counted)
		}
	}
	s := SummarizeOutput(es, nil, 0, 1<<62)
	if s.CarriedTokens != 150 || s.CarriedRequests != 4 {
		t.Errorf("summary: %+v", s)
	}
}

func TestSummarizeOutput(t *testing.T) {
	p := func(r LedgerRequest, phase, effort string) LedgerRequest { r.Phase, r.Effort = phase, effort; return r }
	reqs := []LedgerRequest{
		p(lout(0, 1000, lptr(600), 300, 100), "prompt", "high"),
		p(lout(1, 500, nil, 700, 0), "tool_result", "medium"),
		p(lwith(lout(2, 200, nil, 0, 0), func(r *LedgerRequest) { r.HasSizes = false }), "", ""),
		p(lout(9_999, 999, lptr(1), 0, 0), "prompt", "high"), // outside the window
	}
	es, ratios := OutputLedger(reqs)
	s := SummarizeOutput(es, ratios, 0, 60_000*100)
	want := OutputBucket{Key: "total", Requests: 3, Output: 1700, Thinking: 900, ThinkingEstimated: 300, Text: 300 + 200, ToolInput: 100, Unsplit: 200}
	if s.Total != want {
		t.Errorf("total: %+v", s.Total)
	}
	if len(s.ByPhase) != 3 || s.ByPhase[0].Key != "prompt" || s.ByPhase[2].Key != Unknown {
		t.Errorf("by phase: %+v", s.ByPhase)
	}
	if len(s.ByEffort) != 3 || s.ByEffort[0].Key != "high" {
		t.Errorf("by effort: %+v", s.ByEffort)
	}
}

// One token over the 3x line is a rebuild.
func TestRebuildJustOverFactor(t *testing.T) {
	next := lwith(lreq(2, 0, 30_000), func(r *LedgerRequest) { r.Input = 32_010 + 9_999 - 30_000 })
	got := RebuildLedger([]LedgerRequest{lreq(0, 0, 30_000), lreq(1, 30_000, 2_000), next})
	if len(got) != 2 || got[1].NewContent != 9_999 || got[1].Cause != CauseUnexplained {
		t.Errorf("got %+v", got)
	}
}

func TestOpusSonnetToggles(t *testing.T) {
	big := int64(100000)
	reqs := []LedgerRequest{
		{SessionKey: "s1", TS: 1000, Model: "claude-opus-5-5", CacheRead: big, CacheWrite1h: 100},
		{SessionKey: "s1", TS: 2000, Model: "claude-sonnet-5-5", CacheWrite1h: big},                // opus -> sonnet
		{SessionKey: "s1", TS: 3000, Model: "claude-opus-5-5", CacheWrite5m: big, CacheWrite1h: 0}, // sonnet -> opus
		{SessionKey: "s2", TS: 1000, Model: "claude-sonnet-5-5", CacheRead: big},
		{SessionKey: "s2", TS: 2000, Model: "claude-haiku-5-5", CacheWrite1h: big}, // not opus/sonnet
		{SessionKey: "s3", TS: 1000, Model: "claude-opus-5-5", CacheRead: big},
		{SessionKey: "s3", TS: 9000, Model: "claude-sonnet-5-5", CacheWrite1h: big}, // outside the window
	}
	for i := range reqs {
		reqs[i].Plan = "subscription"
	}
	p := OpusSonnetToggles(RebuildLedger(reqs), 0, 5000)
	if p.Switches != 2 || p.Sessions != 1 || p.Tokens != 2*big || len(p.Entries) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Entries[0].FromModel != "claude-opus-5-5" || p.Entries[0].Write1h != big || p.Entries[1].Write1h != 0 {
		t.Errorf("entries %+v", p.Entries)
	}
}
