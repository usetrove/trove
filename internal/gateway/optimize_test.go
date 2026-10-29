package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// capture is an upstream that remembers the last request body it received.
type capture struct {
	mu     sync.Mutex
	body   []byte
	length string
}

func (c *capture) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b := new(bytes.Buffer)
		_, _ = b.ReadFrom(r.Body)
		c.mu.Lock()
		c.body, c.length = b.Bytes(), r.Header.Get("Content-Length")
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		// Usage with no cache writes, so the request can be priced exactly.
		_, _ = w.Write([]byte(`{"id":"msg_X","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1000,"cache_read_input_tokens":3000,"output_tokens":20}}`))
	})
}

func (c *capture) got() ([]byte, string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.body, c.length
}

func engineWith(states map[string]string, pipeline ...optimize.Optimizer) func(*Options) {
	return func(o *Options) {
		cfg := config.Default()
		for k, v := range states {
			cfg.Optimizers[k] = v
		}
		o.Optimizer = optimize.New(optimize.Options{Config: cfg, Pipeline: pipeline, Getenv: func(string) string { return "" }})
	}
}

// noisyRequest has ANSI codes and a repeated-line run inside a tool result.
func noisyRequest() []byte {
	out := `\u001b[31mSENTINEL_ERR\u001b[0m\n` + strings.Repeat(`SENTINEL_RETRY connecting to the cache server again\n`, 30) + `done`
	return []byte(`{"model":"claude-opus-5-5","system":"SENTINEL_SYSTEM","messages":[` +
		`{"role":"user","content":"SENTINEL_USER"},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"` + out + `"}]}]}`)
}

func TestOptimizerOnForwardsTheOptimizedBodyAndRecordsIt(t *testing.T) {
	up := &capture{}
	g := newRigWith(t, up.handler(t), engineWith(map[string]string{"noise_normalizer": "on"}, optimize.NoiseNormalizer{}))
	orig := noisyRequest()
	do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, orig)
	row := g.rec.wait(t)

	body, length := up.got()
	if bytes.Equal(body, orig) || len(body) >= len(orig) {
		t.Fatalf("upstream should receive the smaller, cleaned request:\n%s", body)
	}
	if strconv.Itoa(len(body)) != length {
		t.Errorf("Content-Length %s does not match the forwarded body (%d bytes)", length, len(body))
	}
	if strings.Contains(string(body), `\u001b`) || !strings.Contains(string(body), "(repeated 30 times)") {
		t.Errorf("tool result was not cleaned: %s", body)
	}
	if row.TokensRemoved <= 0 || !strings.Contains(string(row.OptimizationsApplied), `"name":"noise_normalizer"`) ||
		!strings.Contains(string(row.OptimizationsApplied), `"state":"on"`) {
		t.Errorf("row does not say what ran: removed=%d applied=%s", row.TokensRemoved, row.OptimizationsApplied)
	}
	if row.CostDiffUSD == nil || *row.CostDiffUSD <= 0 {
		t.Errorf("cost difference not recorded: %v", row.CostDiffUSD)
	}
	for name, col := range map[string][]byte{"optimizations_applied": row.OptimizationsApplied, "findings": row.Findings, "shadow": row.ShadowSavings} {
		if strings.Contains(string(col), "SENTINEL") {
			t.Errorf("prompt text reached %s: %s", name, col)
		}
	}
	assertNoSecrets(t, g)
}

func TestOptimizerShadowForwardsTheOriginalAndReportsTheSameFigure(t *testing.T) {
	orig := noisyRequest()
	figure := map[string]int64{}
	for _, state := range []string{"shadow", "on"} {
		up := &capture{}
		g := newRigWith(t, up.handler(t), engineWith(map[string]string{"noise_normalizer": state}, optimize.NoiseNormalizer{}))
		do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, orig)
		row := g.rec.wait(t)
		body, _ := up.got()
		if (state == "shadow") != bytes.Equal(body, orig) {
			t.Errorf("%s: forwarded body changed=%v", state, !bytes.Equal(body, orig))
		}
		if !strings.Contains(string(row.OptimizationsApplied), `"state":"`+state+`"`) {
			t.Errorf("%s: applied=%s", state, row.OptimizationsApplied)
		}
		var rep struct {
			Optimizers []optimize.Applied `json:"optimizers"`
		}
		if err := json.Unmarshal(row.OptimizationsApplied, &rep); err != nil || len(rep.Optimizers) != 1 {
			t.Fatalf("%s: %v %s", state, err, row.OptimizationsApplied)
		}
		figure[state] = rep.Optimizers[0].TokensRemoved
		if state == "shadow" && row.TokensRemoved != 0 {
			t.Errorf("shadow must not count tokens as removed, got %d", row.TokensRemoved)
		}
	}
	if figure["shadow"] == 0 || figure["shadow"] != figure["on"] {
		t.Errorf("shadow and on disagree: %v", figure)
	}
}

type boom struct{ optimize.NoiseNormalizer }

func (boom) Transform(context.Context, *optimize.Request) (*optimize.Request, optimize.Report) {
	panic("optimizer bug")
}

func TestOptimizerPanicForwardsTheOriginalRequest(t *testing.T) {
	up := &capture{}
	g := newRigWith(t, up.handler(t), engineWith(map[string]string{"noise_normalizer": "on"}, boom{}))
	orig := noisyRequest()
	r, _ := do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, orig)
	row := g.rec.wait(t)
	body, _ := up.got()
	if r.StatusCode != 200 || !bytes.Equal(body, orig) {
		t.Fatalf("a panicking optimizer must not affect the request: status=%d changed=%v", r.StatusCode, !bytes.Equal(body, orig))
	}
	if !strings.Contains(string(row.OptimizationsApplied), `"failed":"panic"`) || row.TokensRemoved != 0 {
		t.Errorf("failure not recorded: %s", row.OptimizationsApplied)
	}
}

func TestOptimizerDefaultsOffAndIsAPurePassThrough(t *testing.T) {
	up := &capture{}
	g := newRigWith(t, up.handler(t), engineWith(nil, optimize.NoiseNormalizer{}))
	orig := noisyRequest()
	do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, orig)
	row := g.rec.wait(t)
	if body, _ := up.got(); !bytes.Equal(body, orig) || len(row.OptimizationsApplied) != 0 || row.SessionKey == "" {
		t.Errorf("default-off must be a pure pass-through: applied=%s key=%q", row.OptimizationsApplied, row.SessionKey)
	}
}

func TestOptimizerSessionKeyIsDerivedFromTheOriginalBody(t *testing.T) {
	orig := noisyRequest()
	var keys []string
	for _, states := range []map[string]string{nil, {"noise_normalizer": "on"}} {
		g := newRigWith(t, (&capture{}).handler(t), engineWith(states, optimize.NoiseNormalizer{}))
		do(t, "POST", g.gw.URL+"/anthropic/v1/messages", anthropicHdr, orig)
		keys = append(keys, g.rec.wait(t).SessionKey)
	}
	if keys[0] == "" || keys[0] != keys[1] {
		t.Errorf("session key changed when optimizing: %v", keys)
	}
}
