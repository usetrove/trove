package analyze

import "testing"

// subReqs builds one subagent's requests: the first writes the prompt, each later one comes after the
// given pause (minutes). After a pause past the lifetime in effect the prompt is rewritten in full;
// otherwise it is read.
func subReqs(session, agent string, ttlMin int, gaps []int, oneHour bool) []LedgerRequest {
	const prompt = 40_000
	ts := int64(1_000_000_000)
	w := func(n int64) (int64, int64) {
		if oneHour {
			return 0, n
		}
		return n, 0
	}
	w5, w1 := w(prompt)
	out := []LedgerRequest{{SessionKey: session, AgentKey: agent, Sidechain: true, TS: ts, Model: "claude-sonnet-5-5", CacheWrite5m: w5, CacheWrite1h: w1}}
	for i, g := range gaps {
		ts += int64(g) * 60_000
		r := LedgerRequest{SessionKey: session, AgentKey: agent, Sidechain: true, TS: ts, Turn: i + 1, Model: "claude-sonnet-5-5"}
		if g > ttlMin {
			r.CacheWrite5m, r.CacheWrite1h = w(prompt)
		} else {
			r.CacheRead = prompt
			r.CacheWrite5m, r.CacheWrite1h = w(200)
		}
		out = append(out, r)
	}
	return out
}

func TestSubagentTTL(t *testing.T) {
	main := []LedgerRequest{
		{SessionKey: "s1", TS: 1, Model: "claude-sonnet-5-5", CacheWrite1h: 50_000},
		{SessionKey: "s1", TS: 30 * 60_000, Model: "claude-sonnet-5-5", CacheRead: 50_000},
	}
	cases := []struct {
		name                 string
		reqs                 []LedgerRequest
		requests             int
		rebuilds, rebuildTok int64
		reads, readTok       int64
		w1                   int64
	}{
		{name: "no subagents", reqs: main},
		{name: "short gaps only", reqs: append(append([]LedgerRequest{}, main...), subReqs("s1", "a1", 5, []int{1, 2, 3, 1}, false)...),
			requests: 5},
		{name: "long gaps on five minutes", reqs: subReqs("s1", "a1", 5, []int{1, 20, 45, 2, 90}, false),
			requests: 6, rebuilds: 2, rebuildTok: 80_000},
		{name: "long gaps on one hour", reqs: subReqs("s1", "a1", 60, []int{1, 20, 45, 2}, true),
			requests: 5, reads: 2, readTok: 80_000, w1: 40_000 + 4*200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := SubagentTTL(c.reqs)
			tot := s.Total()
			if s.Requests != c.requests || tot.Requests != c.requests {
				t.Fatalf("requests = %d (%d by model), want %d", s.Requests, tot.Requests, c.requests)
			}
			if int64(tot.GapRebuilds) != c.rebuilds || tot.GapRebuildTokens != c.rebuildTok {
				t.Errorf("gap rebuilds = %d (%d tokens), want %d (%d)", tot.GapRebuilds, tot.GapRebuildTokens, c.rebuilds, c.rebuildTok)
			}
			if int64(tot.GapReads) != c.reads || tot.GapReadTokens != c.readTok {
				t.Errorf("gap reads = %d (%d tokens), want %d (%d)", tot.GapReads, tot.GapReadTokens, c.reads, c.readTok)
			}
			if tot.Write1h != c.w1 {
				t.Errorf("1h writes = %d, want %d", tot.Write1h, c.w1)
			}
		})
	}
}

func TestSubagentTTLIsDeterministic(t *testing.T) {
	a := append(subReqs("s2", "a2", 5, []int{30, 30}, false), subReqs("s1", "a1", 5, []int{10}, false)...)
	b := append(subReqs("s1", "a1", 5, []int{10}, false), subReqs("s2", "a2", 5, []int{30, 30}, false)...)
	if x, y := SubagentTTL(a).Total(), SubagentTTL(b).Total(); x != y {
		t.Fatalf("order changed the result: %+v vs %+v", x, y)
	}
}
