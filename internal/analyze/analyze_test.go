package analyze

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/pricing"
)

func TestCleanSessionHasNoFindings(t *testing.T) {
	a := newTestAnalyzer(t, nil)
	for i, fs := range run(t, a, "clean", cleanSession()...) {
		if len(fs) != 0 {
			t.Fatalf("turn %d of the clean session produced findings: %+v", i, fs)
		}
	}
}

func TestEstimatesSumToReportedTotal(t *testing.T) {
	body := req{
		system: []string{pad("system", 3000), "Contents of /work/CLAUDE.md:\n" + pad("rules", 2400)},
		tools:  []string{"Read", "Bash"},
		msgs: []message{
			user(text("go")),
			assistant(text("ok"), toolUse("a", "Read"), toolUse("b", "Bash")),
			user(toolResult("a", pad("file", 1800), false), toolResult("b", pad("shell", 700), false)),
		},
	}.body()
	a := newTestAnalyzer(t, nil)
	out := a.Analyze(turn{body: body, write: 1000}.input(t, "s"))
	var e Estimate
	if err := json.Unmarshal(out.Estimates, &e); err != nil {
		t.Fatal(err)
	}
	want := bodyTokens(t, body)
	var sum int64
	for _, v := range e.Categories {
		sum += v
	}
	if !e.Calibrated || e.TotalTokens != want || sum != want {
		t.Fatalf("calibrated=%v total=%d sum=%d, want %d: %v", e.Calibrated, e.TotalTokens, sum, want, e.Categories)
	}
	for _, k := range []string{CatSystem, CatContextFile, CatToolSchemas, CatHistory, "tool_results:Read", "tool_results:Bash"} {
		if e.Categories[k] == 0 {
			t.Errorf("category %q missing from %v", k, e.Categories)
		}
	}
	if e.Label != "estimated" {
		t.Errorf("label = %q", e.Label)
	}
}

func TestEstimatesSumForOddTotals(t *testing.T) {
	body := req{system: []string{pad("s", 997)}, tools: []string{"A", "B", "C"}, msgs: []message{user(text(pad("u", 1013)))}}.body()
	a := newTestAnalyzer(t, nil)
	for _, total := range []int64{1, 7, 301, 2999} {
		in := turn{body: body}.input(t, "s")
		in.Usage.UncachedInput = total
		var e Estimate
		_ = json.Unmarshal(a.Analyze(in).Estimates, &e)
		var sum int64
		for _, v := range e.Categories {
			sum += v
		}
		if sum != total {
			t.Errorf("total %d: categories sum to %d (%v)", total, sum, e.Categories)
		}
	}
}

func TestUnreportedUsageIsNotCalibrated(t *testing.T) {
	body := req{system: []string{pad("s", 800)}, msgs: []message{user(text("hi"))}}.body()
	in := turn{body: body}.input(t, "s")
	in.Reported, in.Usage.UncachedInput = false, 0
	var e Estimate
	_ = json.Unmarshal(newTestAnalyzer(t, nil).Analyze(in).Estimates, &e)
	if e.Calibrated || e.TotalTokens == 0 {
		t.Fatalf("expected an uncalibrated prior-based estimate, got %+v", e)
	}
}

func TestStatelessDetectors(t *testing.T) {
	big := pad("output", 4000)
	cases := []struct {
		name string
		det  string
		mod  func(*Options)
		msgs []message
		sys  []string
	}{
		{
			name: "duplicate result", det: DetDuplicate,
			msgs: []message{
				user(text("go")), assistant(toolUse("a", "Read")), user(toolResult("a", big, false)),
				assistant(toolUse("b", "Read")), user(toolResult("b", big, false)),
			},
		},
		{
			name: "ansi codes", det: DetNoisy,
			msgs: []message{
				user(text("go")), assistant(toolUse("a", "Bash")),
				user(toolResult("a", strings.Repeat("\x1b[32mgreen text here\x1b[0m\n", 40), false)),
			},
		},
		{
			name: "progress bar", det: DetNoisy,
			msgs: []message{
				user(text("go")), assistant(toolUse("a", "Bash")),
				user(toolResult("a", progressOutput(), false)),
			},
		},
		{
			name: "identical lines", det: DetNoisy,
			msgs: []message{
				user(text("go")), assistant(toolUse("a", "Bash")),
				user(toolResult("a", "start\n"+strings.Repeat("retrying connection to host\n", 30)+"done", false)),
			},
		},
		{
			name: "passing tests", det: DetNoisy,
			msgs: []message{
				user(text("go")), assistant(toolUse("a", "Bash")),
				user(toolResult("a", passingOutput(60)+"FAIL: TestX", false)),
			},
		},
		{
			name: "oversized", det: DetOversized, mod: func(o *Options) { o.OversizedToolTokens = 500 },
			msgs: []message{
				user(text("go")), assistant(toolUse("a", "Read")), user(toolResult("a", pad("huge", 12000), false)),
			},
		},
		{
			name: "context files", det: DetContextFiles,
			sys:  []string{"You are a coding agent.", "Contents of /repo/CLAUDE.md (project instructions):\n" + pad("rules", 6000)},
			msgs: []message{user(text("hi"))},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newTestAnalyzer(t, c.mod)
			sys := c.sys
			if sys == nil {
				sys = []string{pad("system", 4000)}
			}
			fs := analyzeOne(t, a, turn{body: req{system: sys, tools: []string{"Read", "Bash"}, msgs: c.msgs}.body(), output: 2000}.input(t, "s"))
			f := find(fs, c.det)
			if f == nil {
				t.Fatalf("%s did not fire: %+v", c.det, fs)
			}
			if f.Tokens <= 0 || f.USD == nil || *f.USD <= 0 || f.Label != "estimated" {
				t.Errorf("finding lacks tokens, dollars or label: %+v", f)
			}
		})
	}
}

func progressOutput() string {
	var sb strings.Builder
	sb.WriteString("downloading dependencies\n")
	for i := 1; i <= 40; i++ {
		sb.WriteString("[" + strings.Repeat("=", i/2) + ">" + strings.Repeat(" ", 20-i/2) + "] ")
		sb.WriteString(strings.Repeat("0", 0))
		sb.WriteString(itoa(i*2) + "%\r")
	}
	sb.WriteString("done\n")
	return sb.String()
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func passingOutput(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString("--- PASS: TestCase" + itoa(i) + " (0.00s)\n")
	}
	return sb.String()
}

func TestStatelessDetectorsStayQuietOnPlainOutput(t *testing.T) {
	a := newTestAnalyzer(t, nil)
	msgs := []message{
		user(text("go")), assistant(toolUse("a", "Read")), user(toolResult("a", pad("one", 3000), false)),
		assistant(toolUse("b", "Read")), user(toolResult("b", pad("two", 3000), false)),
	}
	fs := analyzeOne(t, a, turn{body: req{system: []string{pad("sys", 6000)}, tools: []string{"Read"}, msgs: msgs}.body(), output: 2000}.input(t, "s"))
	if len(fs) != 0 {
		t.Fatalf("plain output produced findings: %+v", fs)
	}
}

func TestPassingTestsBelowThresholdAreNotNoise(t *testing.T) {
	if n := measureNoise(passingOutput(10)); n.Total != 0 {
		t.Fatalf("10 passing lines counted as noise: %+v", n)
	}
	if n := measureNoise(passingOutput(60)); n.Pass == 0 {
		t.Fatalf("60 passing lines not counted: %+v", n)
	}
}

func TestUnusedToolSchemas(t *testing.T) {
	a := newTestAnalyzer(t, nil)
	sys, tools := []string{pad("system", 4000)}, []string{"Read", "Edit", "Glob"}
	conv := []message{user(text("go"))}
	var last []Finding
	for i := 1; i <= 6; i++ {
		last = analyzeOne(t, a, turn{body: req{system: sys, tools: tools, msgs: conv}.body(), output: 900}.input(t, "s"))
		if i < 5 && find(last, DetUnusedTools) != nil {
			t.Fatalf("fired after only %d requests", i)
		}
		id := "t" + itoa(i)
		conv = append(conv, assistant(toolUse(id, "Read")), user(toolResult(id, "small "+id, false)))
	}
	f := find(last, DetUnusedTools)
	if f == nil {
		t.Fatalf("not reported after 6 requests: %+v", last)
	}
	names, _ := f.Detail["tool_names"].([]any)
	if len(names) != 2 || names[0] != "Edit" || names[1] != "Glob" {
		t.Fatalf("tool names = %v, want [Edit Glob]", names)
	}
}

func TestExpensiveModelSmallTask(t *testing.T) {
	a := newTestAnalyzer(t, nil)
	body := req{system: []string{pad("s", 800)}, msgs: []message{user(text("rename a variable"))}}.body()
	fs := analyzeOne(t, a, turn{body: body, output: 120}.input(t, "s"))
	f := find(fs, DetExpensive)
	if f == nil || f.Confidence != "low" || f.USD == nil || *f.USD <= 0 {
		t.Fatalf("want a low-confidence suggestion with dollars, got %+v", f)
	}
	// A cheap model or a large turn stays quiet.
	cheap := req{model: "claude-haiku-4-5", system: []string{pad("s", 800)}, msgs: []message{user(text("x"))}}.body()
	if fs := analyzeOne(t, a, turn{body: cheap, output: 120}.input(t, "s2")); find(fs, DetExpensive) != nil {
		t.Error("fired for a cheap model")
	}
	if fs := analyzeOne(t, a, turn{body: body, output: 4000}.input(t, "s3")); find(fs, DetExpensive) != nil {
		t.Error("fired for a large output")
	}
}

func TestLongSessionDrift(t *testing.T) {
	a := newTestAnalyzer(t, func(o *Options) { o.DriftContextTokens = 3000; o.DriftMinRequests = 8 })
	sys := []string{pad("system", 8000)}
	conv := []message{user(text("fix the build"))}
	var last []Finding
	for i := 1; i <= 14; i++ {
		last = analyzeOne(t, a, turn{body: req{system: sys, tools: []string{"Bash"}, msgs: conv}.body(), output: 900}.input(t, "s"))
		id := "t" + itoa(i)
		// From turn 7 the agent keeps failing and repeating itself.
		failing := i >= 7
		result := pad("ok"+id, 300)
		if failing {
			result = pad("error"+id, 300)
		}
		conv = append(conv, assistant(text(pad("thinking"+id, 600)), toolUse(id, "Bash")), user(toolResult(id, result, failing)))
		if failing { // a second failing attempt in the same turn
			id2 := id + "b"
			conv = append(conv, assistant(toolUse(id2, "Bash")), user(toolResult(id2, pad("err2"+id, 300), true)))
		}
	}
	f := find(last, DetDrift)
	if f == nil || f.Confidence != "low" || f.USD == nil {
		t.Fatalf("drift not reported at the end of a struggling session: %+v", last)
	}
	// The same session without failures stays quiet.
	b := newTestAnalyzer(t, func(o *Options) { o.DriftContextTokens = 3000; o.DriftMinRequests = 8 })
	conv = []message{user(text("fix the build"))}
	for i := 1; i <= 14; i++ {
		last = analyzeOne(t, b, turn{body: req{system: sys, tools: []string{"Bash"}, msgs: conv}.body(), output: 900}.input(t, "s"))
		id := "t" + itoa(i)
		conv = append(conv, assistant(text(pad("thinking"+id, 600)), toolUse(id, "Bash")), user(toolResult(id, pad("ok"+id, 300), false)))
	}
	if find(last, DetDrift) != nil {
		t.Fatal("drift fired without any errors or repeats")
	}
}

// cacheBase is a cached first turn; each case breaks the second turn in one way.
func cacheBase(t *testing.T) (system, tools []string, first []message, firstTurn turn) {
	t.Helper()
	system = []string{pad("system", 8000), "Today's date is 2026-10-28.\nWorking directory: /home/ann/proj-a\n" + pad("tail", 400)}
	tools = []string{"Read", "Bash"}
	first = []message{user(text("hello"))}
	b := req{system: system, tools: tools, msgs: first, caching: true}.body()
	firstTurn = turn{body: b, write: bodyTokens(t, b)}
	return
}

func TestCacheMissReasons(t *testing.T) {
	second := func(first []message) []message {
		return append(append([]message{}, first...), assistant(text("hi")), user(text("and now?")))
	}
	replaceTail := func(s []string, old, repl string) []string {
		return []string{s[0], strings.Replace(s[1], old, repl, 1)}
	}
	cases := []struct {
		name  string
		want  string
		build func(system, tools []string, first []message) req
		at    time.Duration
	}{
		{"volatile date", ReasonVolatile, func(s, tl []string, f []message) req {
			return req{system: replaceTail(s, "2026-10-28", "2026-10-29"), tools: tl, msgs: second(f), caching: true}
		}, 30 * time.Second},
		{"volatile path", ReasonVolatile, func(s, tl []string, f []message) req {
			return req{system: replaceTail(s, "proj-a", "proj-b"), tools: tl, msgs: second(f), caching: true}
		}, 30 * time.Second},
		{"tools reordered", ReasonTools, func(s, tl []string, f []message) req {
			return req{system: s, tools: []string{tl[1], tl[0]}, msgs: second(f), caching: true}
		}, 30 * time.Second},
		{"tool added", ReasonTools, func(s, tl []string, f []message) req {
			return req{system: s, tools: append(append([]string{}, tl...), "Extra"), msgs: second(f), caching: true}
		}, 30 * time.Second},
		{"ttl expired", ReasonTTL, func(s, tl []string, f []message) req {
			return req{system: s, tools: tl, msgs: second(f), caching: true}
		}, 11 * time.Minute},
		{"no markers", ReasonNoMarkers, func(s, tl []string, f []message) req {
			return req{system: s, tools: tl, msgs: second(f), caching: false}
		}, 30 * time.Second},
		{"model switched", ReasonModel, func(s, tl []string, f []message) req {
			return req{model: "claude-opus-5-5", system: s, tools: tl, msgs: second(f), caching: true}
		}, 30 * time.Second},
		{"unknown", ReasonUnknown, func(s, tl []string, f []message) req {
			return req{system: s, tools: tl, msgs: second(f), caching: true}
		}, 30 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sys, tools, first, t1 := cacheBase(t)
			a := newTestAnalyzer(t, nil)
			analyzeOne(t, a, t1.input(t, "s"))
			body := c.build(sys, tools, first).body()
			// The provider read nothing and rewrote the whole prompt.
			fs := analyzeOne(t, a, turn{at: c.at, body: body, write: bodyTokens(t, body)}.input(t, "s"))
			if got := missReason(fs); got != c.want {
				t.Fatalf("reason = %q, want %q (findings %+v)", got, c.want, fs)
			}
			f := find(fs, DetCacheMiss)
			if f.Tokens <= 0 || f.USD == nil || *f.USD <= 0 {
				t.Errorf("a miss must be priced: %+v", f)
			}
		})
	}
}

func TestVolatileMissRecordsCategoryAndPositionOnly(t *testing.T) {
	sys, tools, first, t1 := cacheBase(t)
	a := newTestAnalyzer(t, nil)
	analyzeOne(t, a, t1.input(t, "s"))
	s2 := []string{sys[0], strings.Replace(sys[1], "2026-10-28", "2026-10-29", 1)}
	body := req{system: s2, tools: tools, msgs: append(append([]message{}, first...), assistant(text("hi")), user(text("more"))), caching: true}.body()
	out := a.Analyze(turn{at: time.Minute, body: body, write: bodyTokens(t, body)}.input(t, "s"))
	f := find(decodeFindings(t, out), DetCacheMiss)
	if f.Detail["content"] != "date" {
		t.Fatalf("content category = %v, want date", f.Detail["content"])
	}
	if strings.Contains(string(out.Findings), "2026-10") {
		t.Fatalf("the changed text leaked into the finding: %s", out.Findings)
	}
	if _, ok := f.Detail["segment_index"]; !ok {
		t.Error("position missing")
	}
}

func TestBelowMinimum(t *testing.T) {
	// Haiku 4.5 needs 4,096 tokens; this ~1,500-token prompt cannot be cached,
	// so nothing is read on the second request and caching is not at fault.
	sys := []string{pad("s", 6000)}
	b1 := req{model: "claude-haiku-4-5", system: sys, msgs: []message{user(text("a"))}, caching: true}.body()
	b2 := req{model: "claude-haiku-4-5", system: sys, msgs: []message{user(text("a")), assistant(text("b")), user(text("c"))}, caching: true}.body()
	a := newTestAnalyzer(t, nil)
	analyzeOne(t, a, turn{body: b1}.input(t, "s"))
	fs := analyzeOne(t, a, turn{at: time.Minute, body: b2}.input(t, "s"))
	if got := missReason(fs); got != ReasonBelowMin {
		t.Fatalf("reason = %q, want below_minimum (%+v)", got, fs)
	}
}

func TestMinimumTable(t *testing.T) {
	for model, want := range map[string]int64{
		"claude-opus-4-8": 1024, "claude-opus-5-5": 512, "claude-opus-4-5": 4096, "claude-haiku-4-5-20251001": 4096,
		"claude-sonnet-5-5": 512, "claude-sonnet-4-6": 1024, "claude-opus-4-7": 2048, "claude-unknown-9": 0,
	} {
		if got := minCacheable("anthropic", model); got != want {
			t.Errorf("%s: minimum %d, want %d", model, got, want)
		}
	}
	if minCacheable("openai", "gpt-5.6-sol") != 1024 || minCacheable("openai", "gpt-5.4") != 0 {
		t.Error("openai minimums wrong")
	}
}

// Hand calculation. Segments (bytes): system 2000, user 2000, two tool calls
// of 4, two tool results of 4000 identical. Total 12008 bytes. The provider
// reports 3002 tokens, so exactly 4 bytes per token: system 500, user 500,
// call 1, result 1000, call 1, result 1000. The second result is the
// duplicate: 1000 tokens spanning prompt tokens [2002, 3002).
func shadowTurn(t *testing.T, read, write int64) Input {
	t.Helper()
	dup := strings.Repeat("0123", 1000)
	body := req{
		system: []string{strings.Repeat("abcd", 500)},
		msgs: []message{
			user(text(strings.Repeat("wxyz", 500))), assistant(toolUse("a1", "Read")), user(toolResult("a1", dup, false)),
			assistant(toolUse("b1", "Read")), user(toolResult("b1", dup, false)),
		},
	}.body()
	if got := bodyTokens(t, body); got != 3002 {
		t.Fatalf("fixture is %d tokens, want 3002", got)
	}
	return turn{body: body, read: read, write: write, output: 2000}.input(t, "s")
}

func TestShadowSavingsMatchHandCalculation(t *testing.T) {
	cases := []struct {
		name        string
		read, write int64
		wantUSD     float64
	}{
		// all 1000 tokens uncached at $5/M
		{"uncached", 0, 0, 1000 * 5e-6},
		// read covers [0,2500): 498 duplicate tokens at $0.50/M, 502 uncached at $5/M
		{"half cached", 2500, 0, 498*0.5e-6 + 502*5e-6},
		// read [0,2002), 5m write [2002,2502), uncached [2502,3002): 500 at $6.25/M + 500 at $5/M
		{"cache write", 2002, 500, 500*6.25e-6 + 500*5e-6},
		// the whole prompt was read from cache
		{"all cached", 3002, 0, 1000 * 0.5e-6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newTestAnalyzer(t, nil)
			out := a.Analyze(shadowTurn(t, c.read, c.write))
			fs := decodeFindings(t, out)
			f := find(fs, DetDuplicate)
			if f == nil || f.Tokens != 1000 || f.USD == nil || math.Abs(*f.USD-c.wantUSD) > 1e-12 {
				t.Fatalf("finding = %+v (usd %v), want 1000 tokens and $%g", f, deref(f), c.wantUSD)
			}
			var sh Shadow
			if err := json.Unmarshal(out.Shadow, &sh); err != nil {
				t.Fatal(err)
			}
			e, ok := sh.Optimizers[OptDedupe]
			if !ok || e.Tokens != 1000 || e.USD == nil || math.Abs(*e.USD-c.wantUSD) > 1e-12 || sh.Label != "estimated" {
				t.Fatalf("shadow = %+v, want %s with $%g", sh, OptDedupe, c.wantUSD)
			}
		})
	}
}

func deref(f *Finding) any {
	if f == nil || f.USD == nil {
		return nil
	}
	return *f.USD
}

func TestUnpricedModelStillReportsTokens(t *testing.T) {
	in := shadowTurn(t, 0, 0)
	in.Model = "claude-not-in-the-price-list"
	body := strings.Replace(string(in.Body), testModel, "claude-not-in-the-price-list", 1)
	in.Body = []byte(body)
	fs := decodeFindings(t, newTestAnalyzer(t, nil).Analyze(in))
	f := find(fs, DetDuplicate)
	if f == nil || f.Tokens != 1000 || f.USD != nil {
		t.Fatalf("want tokens without dollars, got %+v", f)
	}
}

func TestSessionStateIsBounded(t *testing.T) {
	a := newTestAnalyzer(t, func(o *Options) { o.MaxSessions = 3; o.IdleTimeout = time.Hour })
	body := req{system: []string{pad("s", 900)}, msgs: []message{user(text("x"))}}.body()
	for i := 0; i < 10; i++ {
		a.Analyze(turn{at: time.Duration(i) * time.Second, body: body}.input(t, "sess"+itoa(i)))
		if n := a.Sessions(); n > 3 {
			t.Fatalf("holding %d sessions, cap is 3", n)
		}
	}
	a.Analyze(turn{at: 5 * time.Hour, body: body}.input(t, "late"))
	if n := a.Sessions(); n != 1 {
		t.Fatalf("idle sessions were not evicted: %d held", n)
	}
}

func TestBadInputIsSkippedNotFatal(t *testing.T) {
	a := newTestAnalyzer(t, nil)
	for _, in := range []Input{
		{Provider: "anthropic", Path: "/v1/messages", Body: []byte("not json")},
		{Provider: "anthropic", Path: "/v1/models", Body: []byte("{}")},
		{Provider: "anthropic", Path: "/v1/messages", Body: nil},
		{Provider: "openai", Path: "/v1/embeddings", Body: []byte(`{"model":"x"}`)},
	} {
		if out := a.Analyze(in); out != nil {
			t.Errorf("%s %s should be skipped, got %+v", in.Provider, in.Path, out)
		}
	}
	big := New(Options{MaxBody: 10})
	if big.Analyze(Input{Provider: "anthropic", Path: "/v1/messages", Body: []byte(strings.Repeat("x", 100))}) != nil {
		t.Error("oversized body was analyzed")
	}
}

func TestOpenAIShapes(t *testing.T) {
	dup := pad("out", 2000)
	chat, _ := json.Marshal(map[string]any{
		"model": "gpt-5.6-sol",
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "shell"}}},
		"messages": []any{
			map[string]any{"role": "system", "content": pad("sys", 3000)},
			map[string]any{"role": "user", "content": "run it"},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c1", "function": map[string]any{"name": "shell", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": "c1", "content": dup},
			map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c2", "function": map[string]any{"name": "shell", "arguments": "{}"}}}},
			map[string]any{"role": "tool", "tool_call_id": "c2", "content": dup},
		},
	})
	resp, _ := json.Marshal(map[string]any{
		"model": "gpt-5.6-sol", "instructions": pad("sys", 3000),
		"input": []any{
			map[string]any{"role": "user", "content": "run it"},
			map[string]any{"type": "function_call", "call_id": "c1", "name": "shell", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c1", "output": dup},
			map[string]any{"type": "function_call", "call_id": "c2", "name": "shell", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "c2", "output": dup},
		},
	})
	for name, c := range map[string]struct {
		path string
		body []byte
	}{"chat": {"/v1/chat/completions", chat}, "responses": {"/v1/responses", resp}} {
		t.Run(name, func(t *testing.T) {
			a := newTestAnalyzer(t, nil)
			in := Input{Provider: "openai", Path: c.path, Body: c.body, SessionKey: "o", Start: t0, Reported: true,
				Usage: usageFor(1800, 2000)}
			out := a.Analyze(in)
			if out == nil {
				t.Fatal("not analyzed")
			}
			f := find(decodeFindings(t, out), DetDuplicate)
			if f == nil || f.USD == nil {
				t.Fatalf("duplicate not found in %s", out.Findings)
			}
			var e Estimate
			_ = json.Unmarshal(out.Estimates, &e)
			if e.Categories["tool_results:shell"] == 0 {
				t.Errorf("tool results not attributed to the tool: %v", e.Categories)
			}
		})
	}
}

// BenchmarkAnalyze shows what analysis costs per request. It runs after the
// response has been delivered, so this is background CPU, not latency.
func BenchmarkAnalyze(b *testing.B) {
	msgs := []message{user(text("go"))}
	for i := 0; i < 40; i++ {
		id := "t" + itoa(i)
		msgs = append(msgs, assistant(toolUse(id, "Read")), user(toolResult(id, pad(id, 6000), false)))
	}
	body := req{system: []string{pad("system", 20000)}, tools: []string{"Read", "Bash", "Grep"}, msgs: msgs, caching: true}.body()
	tbl, _ := pricing.Default()
	a := New(Options{Pricing: tbl})
	in := Input{Provider: "anthropic", Path: "/v1/messages", Body: body, SessionKey: "bench", Start: t0, Reported: true,
		Usage: pricing.Usage{UncachedInput: 70000, Output: 500}}
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		in.Start = t0.Add(time.Duration(i) * time.Second)
		a.Analyze(in)
	}
}
