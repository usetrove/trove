package measure

import (
	"strings"
	"testing"
)

func TestNetGuardRule(t *testing.T) {
	g := NetGuard{MinRequests: 20, MaxFailOpenRate: 0.10}
	cases := []struct {
		name    string
		w       NetWindow
		disable bool
		enough  bool
		reason  string // substring the reason must contain, when disabled
	}{
		{"too few requests is not judged", NetWindow{Requests: 5, TokensRemoved: 0, OverheadTokens: 999, FailOpen: 5}, false, false, ""},
		{"healthy optimizer stays on", NetWindow{Requests: 100, TokensRemoved: 5000, OverheadTokens: 100, FailOpen: 2}, false, true, ""},
		{"negative net saving disables", NetWindow{Requests: 100, TokensRemoved: 100, OverheadTokens: 500, FailOpen: 0}, true, true, "net saving is negative"},
		{"high fail-open disables", NetWindow{Requests: 100, TokensRemoved: 9000, OverheadTokens: 0, FailOpen: 30}, true, true, "fell back to the original"},
		{"fail-open exactly at the limit stays on", NetWindow{Requests: 100, TokensRemoved: 9000, FailOpen: 10}, false, true, ""},
		{"net is checked first", NetWindow{Requests: 100, TokensRemoved: 0, OverheadTokens: 10, FailOpen: 50}, true, true, "net saving is negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := g.Evaluate(c.w)
			if v.Disable != c.disable || v.Enough != c.enough {
				t.Fatalf("got disable=%v enough=%v, want disable=%v enough=%v (%+v)", v.Disable, v.Enough, c.disable, c.enough, v)
			}
			if c.disable && !strings.Contains(v.Reason, c.reason) {
				t.Errorf("reason %q should contain %q", v.Reason, c.reason)
			}
			if !c.disable && v.Reason != "" {
				t.Errorf("an optimizer that stays on must not carry a reason: %q", v.Reason)
			}
		})
	}
}

// The shipped policy must not disable anything on an empty window.
func TestDefaultNetGuardDoesNothingWithoutData(t *testing.T) {
	if v := DefaultNetGuard.Evaluate(NetWindow{}); v.Disable || v.Enough {
		t.Fatalf("empty window must neither disable nor count as data: %+v", v)
	}
}
