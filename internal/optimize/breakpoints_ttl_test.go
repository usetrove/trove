package optimize

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/usetrove/trove/internal/config"
)

const oneHour = `"cache_control":{"type":"ephemeral","ttl":"1h"}`
const fiveMin = `"cache_control":{"type":"ephemeral"}`

func ttlSession(t config.CacheTTL) Session {
	s := custom
	s.CacheTTL = t
	return s
}

func markTTL(t *testing.T, body []byte, ttl config.CacheTTL) (*Request, Report) {
	t.Helper()
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	return CacheBreakpoints{}.TransformSession(context.Background(), r, ttlSession(ttl))
}

func span(r *Request, s Span) []byte { return r.Raw[s.S:s.E] }

func TestCacheBreakpointsDefaultTTLKeepsTodaysBytes(t *testing.T) {
	body := req{system: pad(6000), tools: []string{"bash", "read"}, msgs: []msg{user(text("hi"))}}.body()
	r, _ := ParseRequest(body)
	want, _ := CacheBreakpoints{}.Transform(context.Background(), r)
	for _, ttl := range []config.CacheTTL{{}, {Tools: "5m", System: "5m", Messages: "5m"}} {
		got, rep := markTTL(t, body, ttl)
		if rep.Err != "" || !bytes.Equal(got.Raw, want.Raw) {
			t.Errorf("%+v: a 5-minute lifetime must give the same bytes as before (err %q)", ttl, rep.Err)
		}
	}
}

func TestCacheBreakpointsPerMarkerTTL(t *testing.T) {
	body := req{system: pad(6000), tools: []string{"bash", "read"}, msgs: []msg{user(text("hi"))}}.body()
	out, rep := markTTL(t, body, config.CacheTTL{Tools: "1h", System: "1h"})
	if rep.Err != "" {
		t.Fatal(rep.Err)
	}
	if err := out.Ensure(); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(out.Raw, []byte(`{`+fiveMin+`,"model"`)) {
		t.Errorf("the top-level (messages) marker should stay 5 minutes: %.80s", out.Raw)
	}
	if !bytes.Contains(span(out, out.ToolSpans[1]), []byte(oneHour)) {
		t.Error("the last tool should carry a 1-hour marker")
	}
	if !bytes.Contains(span(out, out.SystemBlocks[0].Span), []byte(oneHour)) {
		t.Error("the system block should carry a 1-hour marker")
	}
	if n := countMarkers(out.Raw); n != 3 {
		t.Errorf("want 3 markers, got %d", n)
	}

	all, _ := markTTL(t, body, config.CacheTTL{Tools: "1h", System: "1h", Messages: "1h"})
	if !bytes.HasPrefix(all.Raw, []byte(`{`+oneHour+`,"model"`)) {
		t.Errorf("the top-level marker should be 1 hour: %.80s", all.Raw)
	}

	toolsOnly, _ := markTTL(t, body, config.CacheTTL{Tools: "1h"})
	if err := toolsOnly.Ensure(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(span(toolsOnly, toolsOnly.ToolSpans[1]), []byte(oneHour)) || bytes.Count(toolsOnly.Raw, []byte(`"ttl"`)) != 1 {
		t.Error("only the tool marker should be 1 hour")
	}
}

func TestCacheBreakpointsRefusesInvalidTTLOrder(t *testing.T) {
	body := req{system: pad(6000), tools: []string{"bash"}, msgs: []msg{user(text("hi"))}}.body()
	for _, ttl := range []config.CacheTTL{{System: "1h"}, {Messages: "1h"}, {Tools: "1h", Messages: "1h"}, {Tools: "2h"}} {
		out, rep := markTTL(t, body, ttl)
		if rep.Err == "" {
			t.Errorf("%+v: an invalid ordering must be refused", ttl)
		}
		if !bytes.Equal(out.Raw, body) {
			t.Errorf("%+v: a refused request must be returned untouched", ttl)
		}
	}
}

func TestCacheBreakpointsTTLIsByteStable(t *testing.T) {
	ttl := config.CacheTTL{Tools: "1h", System: "1h"}
	system := pad(5000)
	var prefix []byte
	for turn := 1; turn <= 20; turn++ {
		body := req{system: system, tools: []string{"bash", "read"}, msgs: sessionTurns(turn)}.body()
		a, _ := markTTL(t, body, ttl)
		b, _ := markTTL(t, body, ttl)
		if !bytes.Equal(a.Raw, b.Raw) {
			t.Fatalf("turn %d: same request, different output", turn)
		}
		mi := bytes.Index(a.Raw, []byte(`"messages"`))
		if prefix == nil {
			prefix = append([]byte{}, a.Raw[:mi]...)
		} else if !bytes.Equal(a.Raw[:mi], prefix) {
			t.Fatalf("turn %d: the cached prefix (tools and system) changed", turn)
		}
		if !bytes.HasSuffix(a.Raw, body[bytes.Index(body, []byte(`"messages"`)):]) {
			t.Fatalf("turn %d: message bytes changed", turn)
		}
	}
}

func TestCacheTTLIsPinnedForTheSession(t *testing.T) {
	cfg := config.Default()
	cfg.Optimizers["cache_breakpoints"] = config.StateOn
	cfg.Optimize.CustomAgents = []string{"sdk"}
	cfg.Optimize.CacheTTL = config.CacheTTL{Tools: "1h", System: "1h"}
	store := &memPins{m: map[string]Pin{}}
	body := req{system: pad(6000), tools: []string{"bash"}, msgs: []msg{user(text("hi"))}}.body()

	first := New(Options{Config: cfg, Pins: store, Getenv: func(string) string { return "" }}).Run(context.Background(), input(body))
	if !bytes.Contains(first.Body, []byte(oneHour)) {
		t.Fatal("the configured 1-hour lifetime was not applied")
	}
	// The user goes back to 5 minutes and the gateway restarts mid-session.
	cfg.Optimize.CacheTTL = config.CacheTTL{}
	e2 := New(Options{Config: cfg, Pins: store, Getenv: func(string) string { return "" }})
	if again := e2.Run(context.Background(), input(body)); !bytes.Equal(again.Body, first.Body) {
		t.Error("a config change reached a running session")
	}
	in := input(body)
	in.SessionKey = "s2"
	if next := e2.Run(context.Background(), in); bytes.Contains(next.Body, []byte(`"ttl"`)) {
		t.Error("a new session should use the new lifetimes")
	}

	raw, _ := json.Marshal(store.m["s1"])
	var back Pin
	if err := json.Unmarshal(raw, &back); err != nil || back.CacheTTL != (config.CacheTTL{Tools: "1h", System: "1h"}) {
		t.Errorf("the pin should round-trip its lifetimes, got %+v (%v)", back.CacheTTL, err)
	}
}
