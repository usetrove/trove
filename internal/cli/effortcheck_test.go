package cli

import (
	"encoding/json"
	"testing"
)

func TestEffortCheckJSONWithNoData(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("optimize", "effort-check", "--json", "--data-dir", t.TempDir()); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err.String())
	}
	var got effortCheckOutput
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out)
	}
	if got.Models == nil || got.Message == "" {
		t.Errorf("want an empty model list and a next step: %+v", got)
	}
}
