package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/optimize"
)

func TestDoctorCapabilities(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nope")
	h := newHarness(t, nil)
	if code := h.run("doctor", "--capabilities", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var got struct {
		VerifiedOn string         `json:"harness_facts_verified_on"`
		Optimizers []optimize.Row `json:"optimizers"`
	}
	if err := json.Unmarshal([]byte(h.out.String()), &got); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, h.out.String())
	}
	if got.VerifiedOn == "" || len(got.Optimizers) != len(optimize.Matrix()) {
		t.Fatalf("report = %+v", got)
	}
	h2 := newHarness(t, nil)
	if code := h2.run("doctor", "--capabilities", "--data-dir", dir); code != 0 || !strings.Contains(h2.out.String(), "hook_repeat_read") {
		t.Fatalf("text output missing optimizers (exit %d): %s", code, h2.out.String())
	}
}
