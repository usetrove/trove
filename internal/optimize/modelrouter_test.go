package optimize

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
)

// routerBody builds a Messages request whose user turns are tool turns ("t") or
// typed turns ("u"), in order.
func routerBody(model, turns string) []byte {
	var msgs []string
	for i, c := range turns {
		if c == 't' {
			id := "tu" + string(rune('a'+i))
			msgs = append(msgs,
				`{"role":"assistant","content":[{"type":"tool_use","id":"`+id+`","name":"Bash","input":{}}]}`,
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"`+id+`","content":"ok"}]}`)
			continue
		}
		if i > 0 {
			msgs = append(msgs, `{"role":"assistant","content":"done"}`)
		}
		msgs = append(msgs, `{"role":"user","content":"do the next thing"}`)
	}
	return []byte(`{"model":"` + model + `","max_tokens":10,"messages":[` + strings.Join(msgs, ",") + `]}`)
}

func TestStickyTurnsMatchesThePlanFormula(t *testing.T) {
	// With the documented multipliers (write 1.25, read 0.1 of input), k = ceil(11.5·r/(1−r)).
	for _, tc := range []struct {
		r    float64
		want int
	}{{1.0 / 3, 6}, {0.6, 18}, {0.5, 12}, {0.2, 3}} {
		k, ok := StickyTurns(1.25*tc.r, 0.1*tc.r, 0.1)
		if !ok || k != tc.want {
			t.Errorf("r=%.3f: k=%d ok=%v, want %d", tc.r, k, ok, tc.want)
		}
		if f := math.Ceil(11.5 * tc.r / (1 - tc.r)); int(f) != tc.want {
			t.Errorf("r=%.3f: plan formula gives %v, want %d", tc.r, f, tc.want)
		}
	}
	if _, ok := StickyTurns(1.25, 0.1, 0.1); ok {
		t.Error("a model that is not cheaper to read must not be routed to")
	}
}

func TestStepDownIsSticky(t *testing.T) {
	for _, tc := range []struct {
		turns string
		k     int
		want  string
	}{
		{"uuu", 3, "---"},
		{"uttt", 3, "-ddd"},
		{"uttu", 3, "-ddd"},   // a typed turn before k turns does not switch back
		{"utttu", 3, "-ddd-"}, // after k turns, a typed turn returns
		{"utttt", 3, "-dddd"}, // tool turns keep it down
		{"utuuu", 2, "-dd--"},
	} {
		in := make([]bool, len(tc.turns))
		for i, c := range tc.turns {
			in[i] = c == 't'
		}
		var got strings.Builder
		for _, d := range StepDown(in, tc.k) {
			if d {
				got.WriteByte('d')
			} else {
				got.WriteByte('-')
			}
		}
		if got.String() != tc.want {
			t.Errorf("StepDown(%s, k=%d) = %s, want %s", tc.turns, tc.k, got.String(), tc.want)
		}
	}
}

func TestModelRouterShadowShowsCacheWriteCost(t *testing.T) {
	at := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	s := Session{RouteTo: "claude-sonnet-5-5", PinnedAt: at}
	m := ModelRouter{}
	body := routerBody("claude-opus-5-5", "ut") // the second turn is the switch
	r, err := ParseRequest(body)
	if err != nil {
		t.Fatal(err)
	}
	out, rep := m.TransformSession(context.Background(), r, s)
	if rep.Route == nil {
		t.Fatal("no route estimate")
	}
	e := rep.Route
	if !e.Switch || e.Model != "claude-sonnet-5-5" || e.Label != "estimated" {
		t.Fatalf("estimate = %+v", e)
	}
	if e.CacheWriteUSD <= 0 || e.StickyTurns < 1 || e.Ratio <= 0 || e.Ratio >= 1 {
		t.Errorf("estimate missing its numbers: %+v", e)
	}
	var got struct{ Model string }
	_ = json.Unmarshal(out.Raw, &got)
	if got.Model != "claude-sonnet-5-5" {
		t.Errorf("forwarded model %q", got.Model)
	}
	// Same history, same bytes.
	r2, _ := ParseRequest(body)
	out2, _ := m.TransformSession(context.Background(), r2, s)
	if string(out.Raw) != string(out2.Raw) {
		t.Error("model_router is not deterministic")
	}
	// A turn that is not a switch carries no cache-write cost.
	r3, _ := ParseRequest(routerBody("claude-opus-5-5", "utt"))
	_, rep3 := m.TransformSession(context.Background(), r3, s)
	if rep3.Route == nil || rep3.Route.Switch || rep3.Route.CacheWriteUSD != 0 {
		t.Errorf("non-switch turn: %+v", rep3.Route)
	}
}

func TestModelRouterLeavesRequestAlone(t *testing.T) {
	at := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		model, to, turns string
	}{
		"no target":      {"claude-opus-5-5", "", "ut"},
		"unpriced":       {"claude-opus-5-5", "not-a-model", "ut"},
		"not cheaper":    {"claude-haiku-5-5", "claude-opus-5-5", "ut"},
		"typed turn":     {"claude-opus-5-5", "claude-sonnet-5-5", "uu"},
		"already target": {"claude-sonnet-5-5", "claude-sonnet-5-5", "ut"},
	} {
		body := routerBody(tc.model, tc.turns)
		r, _ := ParseRequest(body)
		out, _ := ModelRouter{}.TransformSession(context.Background(), r, Session{RouteTo: tc.to, PinnedAt: at})
		if string(out.Raw) != string(body) {
			t.Errorf("%s: request changed", name)
		}
	}
}

func TestEngineRoutesWithoutClaimingSavings(t *testing.T) {
	cfg := config.Default()
	cfg.Optimizers[OptimizerModelRouter] = config.StateShadow
	cfg.Optimize.RouteTo = "claude-sonnet-5-5"
	e := New(Options{Config: cfg, Now: func() time.Time { return time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC) }})
	body := routerBody("claude-opus-5-5", "ut")
	res := e.TransformRequest(context.Background(), RequestIn{Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: body}, Session{Key: "s1"})
	if res.Changed || string(res.Body) != string(body) {
		t.Fatal("shadow mode changed the request")
	}
	if len(res.Applied) != 1 || res.Applied[0].Route == nil || res.Applied[0].TokensRemoved != 0 || res.Applied[0].BytesRemoved != 0 {
		t.Fatalf("applied = %+v", res.Applied)
	}
	if j := string(AppliedJSON(res.Applied, nil)); !strings.Contains(j, `"cache_write_usd_estimated"`) {
		t.Errorf("the recorded column lacks the cache-write estimate: %s", j)
	}
}
