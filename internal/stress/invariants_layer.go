package stress

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/usetrove/trove/internal/optimize"
)

// invariants is layer 3. It proves two things. First, the checkers themselves can fail: each one is handed a
// deliberately broken input and must report the right invariant (a checker that stays silent would make every
// other layer meaningless). Second, the engine is deterministic when many goroutines share one instance.
// Pass/fail only: no savings.
func (e *env) invariants() LayerResult {
	lr := LayerResult{Name: LayerInvariants}
	base := envelope(jstr(strings.Repeat("ok line of output\n", 200)), "")

	// Each control is a broken pair and the invariant that must catch it.
	controls := []struct {
		name string
		run  func() []Violation
		want string
	}{
		{"control-dropped-tool-result-id", func() []Violation {
			broken := bytes.Replace(base, []byte(`"tool_use_id":"toolu_1"`), []byte(`"tool_use_id":"toolu_9"`), 1)
			return CheckRequest(base, broken, 0)
		}, InvPairing},
		{"control-truncated-json", func() []Violation {
			return CheckRequest(base, base[:len(base)-10], 0)
		}, InvValidJSON},
		{"control-grew-past-overhead", func() []Violation {
			// 625 bytes of new visible text: about 179 tokens, far more than the 0 declared.
			grown := bytes.Replace(base, []byte("keep the tests green"), []byte("keep the tests green"+strings.Repeat(" more", 125)), 1)
			return CheckRequest(base, grown, 0)
		}, InvCostBound},
		{"control-early-prefix-edit", func() []Violation {
			prev := base
			cur := bytes.Replace(base, []byte("keep the tests green"), []byte("keep the tests RED"), 1)
			return CheckPrefix(prev, cur)
		}, InvPrefix},
		{"control-recovery-not-a-tool-output", func() []Violation {
			return CheckRecovery(base, [][]byte{[]byte("text the tool never printed")})
		}, InvRecovery},
		{"control-invalid-request-changed", func() []Violation {
			return CheckRequest([]byte("{x"), []byte("{y"), 0)
		}, InvFailOpen},
	}
	for _, c := range controls {
		vs := c.run()
		if !hasInvariant(vs, c.want) {
			e.record(&lr, c.name, []Violation{{c.want, "the checker did not catch a deliberately broken input"}}, base)
			continue
		}
		e.record(&lr, c.name, nil, nil)
	}

	// Concurrent determinism: one shared engine, many goroutines, the same history, the same bytes.
	big := envelope(jstr(strings.Repeat("src/a.go:12: match\n", 5000)), "")
	eng := newCustomEngine(allNames(), &memStore{}, PricingDate)
	const workers = 8
	outs := make([][]byte, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			res := eng.TransformRequest(context.Background(), optimize.RequestIn{
				Provider: "anthropic", Path: "/v1/messages", Header: http.Header{}, Body: big,
			}, optimize.Session{Key: "concurrent-det", Agent: customAgent, TroveRun: true})
			outs[w] = res.Body
		}(w)
	}
	wg.Wait()
	var vs []Violation
	for _, o := range outs[1:] {
		if !bytes.Equal(o, outs[0]) {
			vs = append(vs, Violation{InvDeterministic, fmt.Sprintf("%d concurrent calls disagreed", workers)})
			break
		}
	}
	vs = append(vs, CheckRequest(big, outs[0], 0)...)
	e.record(&lr, "concurrent-determinism", vs, big)

	lr.Notes = append(lr.Notes, "each checker must catch its own broken control; a silent checker fails this layer")
	finish(&lr)
	return lr
}

func hasInvariant(vs []Violation, name string) bool {
	for _, v := range vs {
		if v.Invariant == name {
			return true
		}
	}
	return false
}
