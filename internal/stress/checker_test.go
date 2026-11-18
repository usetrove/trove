package stress

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// TestEveryOptimizerPassesItsChecker is the merge gate for plan item 2.5: every shipped optimizer, run alone, keeps every
// failure line, leaves the cached prefix byte-identical and gives the same bytes on a repeat run. A new optimizer is
// covered as soon as it is added to optimize.Pipeline or optimize.ToolPipeline.
func TestEveryOptimizerPassesItsChecker(t *testing.T) {
	names := checkerNames()
	if len(names) == 0 {
		t.Fatal("no optimizers registered")
	}
	for _, n := range names {
		t.Run(n, func(t *testing.T) {
			c, err := CheckOptimizer(n)
			if err != nil {
				t.Fatal(err)
			}
			if c.Requests == 0 {
				t.Fatal("the checker sent no requests")
			}
			if !c.Passed() {
				t.Errorf("%s failed its checker: %s", n, strings.Join(c.Problems, "; "))
			}
		})
	}
}

// replaceAll is a test optimizer that rewrites every occurrence of old in the request bytes.
type replaceAll struct {
	name, old, repl string
	minMessages     int // act only on requests with at least this many messages
}

func (o replaceAll) Name() string                                     { return o.name }
func (o replaceAll) Tier() optimize.Tier                              { return optimize.TierLossless }
func (o replaceAll) Applies(*optimize.Request, optimize.Session) bool { return true }
func (o replaceAll) Transform(_ context.Context, r *optimize.Request) (*optimize.Request, optimize.Report) {
	if len(r.Messages) < o.minMessages || !bytes.Contains(r.Raw, []byte(o.old)) {
		return r, optimize.Report{}
	}
	var edits []optimize.Edit
	for off := 0; ; {
		j := bytes.Index(r.Raw[off:], []byte(o.old))
		if j < 0 {
			break
		}
		i := off + j
		edits = append(edits, optimize.Edit{Span: optimize.Span{S: i, E: i + len(o.old)}, New: []byte(o.repl)})
		off = i + len(o.old)
	}
	out, err := r.Apply(edits)
	if err != nil {
		return r, optimize.Report{Err: err.Error()}
	}
	return out, optimize.Report{}
}

// singleEngine builds an engine whose only optimizer is o, switched on.
func singleEngine(o optimize.Optimizer, custom bool, rec *memStore) *optimize.Engine {
	cfg := config.Default()
	cfg.Optimize.BudgetMS = 60000
	if custom {
		cfg.Optimize.CustomAgents = []string{customAgent}
	}
	cfg.Optimizers = map[string]string{o.Name(): config.StateOn}
	return optimize.New(optimize.Options{
		Config: cfg, Pipeline: []optimize.Optimizer{o}, Tools: []optimize.Base{},
		Getenv: func(string) string { return "" }, Now: func() time.Time { return PricingDate }, Recovery: rec,
	})
}

// The checker must fail each kind of broken transform, or the gate above proves nothing.
func TestCheckerCatchesBrokenTransforms(t *testing.T) {
	// The engine pins states only for shipped names, so each broken optimizer runs under a shipped name, alone.
	const borrowed = config.OptimizerNoiseNormalizer
	plays := 0
	cases := []struct {
		name string
		mk   func(custom bool, rec *memStore) *optimize.Engine
		want string
	}{
		{"drops failure lines", func(c bool, r *memStore) *optimize.Engine {
			return singleEngine(replaceAll{name: borrowed, old: "FAILED ", repl: "passed "}, c, r)
		}, "failure lines"},
		{"rewrites history", func(c bool, r *memStore) *optimize.Engine {
			// The first user message changes only once the session has history, so the earlier request is no longer a prefix.
			return singleEngine(replaceAll{name: borrowed, old: "from yesterday", repl: "from this week", minMessages: 3}, c, r)
		}, "earlier part of the request"},
		{"differs between runs", func(c bool, r *memStore) *optimize.Engine {
			// Each run of a session gets a different (but internally consistent) rewrite.
			plays++
			repl := []string{"this week", "last week"}[plays%2]
			return singleEngine(replaceAll{name: borrowed, old: "yesterday", repl: repl}, c, r)
		}, "repeat run"},
		{"marks output it never stored", func(c bool, r *memStore) *optimize.Engine {
			// A trim marker in place of a log line, with nothing stored behind it.
			return singleEngine(replaceAll{name: borrowed, old: "GET /healthz 200 1ms",
				repl: "[Trove: 1 lines trimmed; full output sha256:0123456789abcdef]"}, c, r)
		}, "never stored"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := checkWith(tc.name, tc.mk)
			if err != nil {
				t.Fatal(err)
			}
			if c.Passed() {
				t.Fatalf("the checker passed a broken transform: %+v", c)
			}
			if !strings.Contains(strings.Join(c.Problems, "; "), tc.want) {
				t.Errorf("want a problem mentioning %q, got %v", tc.want, c.Problems)
			}
		})
	}
}
