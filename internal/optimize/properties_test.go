package optimize

import (
	"bytes"
	"context"
	"testing"
)

// checkProperties asserts the contract every optimizer owes the cache:
//
//   - determinism: transforming the same bytes twice gives the same bytes
//   - idempotence: transforming the output again changes nothing
//   - no-op: a request the optimizer does not apply to comes back unchanged
//
// applies are requests the optimizer should change; noops should come back
// byte-identical.
func checkProperties(t *testing.T, o Optimizer, sess Session, applies, noops [][]byte) {
	t.Helper()
	run := func(body []byte) ([]byte, bool) {
		t.Helper()
		r, err := ParseRequest(body)
		if err != nil {
			t.Fatalf("fixture does not parse: %v", err)
		}
		if !o.Applies(r, sess) {
			return body, false
		}
		out, rep := o.Transform(context.Background(), r)
		if rep.Err != "" {
			t.Fatalf("transform failed: %s", rep.Err)
		}
		return out.Raw, true
	}
	for i, body := range applies {
		first, ok := run(body)
		if !ok || bytes.Equal(first, body) {
			t.Errorf("applies[%d]: expected a change", i)
			continue
		}
		if second, _ := run(body); !bytes.Equal(first, second) {
			t.Errorf("applies[%d]: not deterministic", i)
		}
		if again, _ := run(first); !bytes.Equal(again, first) {
			t.Errorf("applies[%d]: not idempotent\n first %s\n again %s", i, first, again)
		}
		if _, err := ParseRequest(first); err != nil {
			t.Errorf("applies[%d]: output is not parseable: %v", i, err)
		}
	}
	for i, body := range noops {
		if out, _ := run(body); !bytes.Equal(out, body) {
			t.Errorf("noops[%d]: request changed\n in  %s\n out %s", i, body, out)
		}
	}
}
