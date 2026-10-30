package optimize

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/recovery"
)

type memRecovery struct {
	got  map[string]string
	fail bool
}

func (m *memRecovery) Put(b []byte) (string, error) {
	if m.fail {
		return "", errors.New("disk full")
	}
	if m.got == nil {
		m.got = map[string]string{}
	}
	id := recovery.ID(b)
	m.got[id] = string(b)
	return id, nil
}

func trimmerOn() config.Config {
	cfg := config.Default()
	cfg.Optimize.BudgetMS = 1000 // these tests check trimming, not speed; CI runners under -race are slow
	cfg.Optimizers[config.OptimizerOutputTrimmer] = config.StateOn
	return cfg
}

func TestTrimmerOnWithRecoveryStoresEveryOriginal(t *testing.T) {
	out := goTestOutput(200)
	body := req{msgs: []msg{user(toolResult("t1", out))}}.body()
	store := &memRecovery{}
	e := New(Options{Config: trimmerOn(), Getenv: func(string) string { return "" }, Recovery: store})
	res := e.Run(context.Background(), input(body))
	if !res.Changed || len(res.Body) >= len(body) {
		t.Fatalf("the trimmer should shorten the request: %+v", res.Failures)
	}
	if len(res.Applied) != 1 || res.Applied[0].State != config.StateOn || res.Applied[0].Originals != nil {
		t.Errorf("report: %+v", res.Applied)
	}
	// The marker's hash is the ID, and the stored text is the original, exactly.
	id := recovery.ID([]byte(out))
	if !strings.Contains(string(res.Body), "full output sha256:"+id) {
		t.Fatalf("marker does not carry the output ID %s", id)
	}
	if store.got[id] != out {
		t.Error("the stored output is not the original, byte for byte")
	}
}

func TestTrimmerStoreFailureForwardsOriginal(t *testing.T) {
	body := req{msgs: []msg{user(toolResult("t1", goTestOutput(200)))}}.body()
	e := New(Options{Config: trimmerOn(), Getenv: func(string) string { return "" }, Recovery: &memRecovery{fail: true}})
	res := e.Run(context.Background(), input(body))
	if res.Changed || string(res.Body) != string(body) {
		t.Fatal("when the output cannot be stored, the original request must be forwarded")
	}
	if len(res.Failures) == 0 || res.Failures[0].Class != "recovery" {
		t.Errorf("failure not recorded: %+v", res.Failures)
	}
}

func TestTrimmerShadowNeverStores(t *testing.T) {
	body := req{msgs: []msg{user(toolResult("t1", goTestOutput(200)))}}.body()
	cfg := config.Default()
	cfg.Optimizers[config.OptimizerOutputTrimmer] = config.StateShadow
	store := &memRecovery{}
	e := New(Options{Config: cfg, Getenv: func(string) string { return "" }, Recovery: store})
	res := e.Run(context.Background(), input(body))
	if res.Changed || len(store.got) != 0 {
		t.Fatal("shadow mode must change and store nothing")
	}
}

func TestTrimmerOutputIsDeterministic(t *testing.T) {
	body := req{msgs: []msg{user(toolResult("t1", goTestOutput(200)))}}.body()
	var first []byte
	for i := 0; i < 3; i++ {
		e := New(Options{Config: trimmerOn(), Getenv: func(string) string { return "" }, Recovery: &memRecovery{}})
		res := e.Run(context.Background(), input(body))
		if first == nil {
			first = res.Body
		} else if string(first) != string(res.Body) {
			t.Fatal("the same history must always give byte-identical output")
		}
	}
}
