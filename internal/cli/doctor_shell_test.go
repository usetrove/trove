package cli

import (
	"path/filepath"
	"testing"
)

// The plugin's hooks run through `sh`. Doctor warns when it is missing, and says what to do.
func TestDoctorWarnsWhenShIsMissing(t *testing.T) {
	h := newHarness(t, nil) // no binaries on PATH, so `sh` is not found
	dir := filepath.Join(t.TempDir(), "data")
	if code := h.run("doctor", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("a missing sh is a warning, not a failure: exit %d", code)
	}
	c := find(decodeDoctor(t, h), "plugin shell")
	if c.Status != statusWarn || c.Fix == "" {
		t.Fatalf("missing sh should warn with a fix, got %+v", c)
	}
}

func TestDoctorIsHappyWhenShIsPresent(t *testing.T) {
	h := newHarness(t, nil, "sh")
	dir := filepath.Join(t.TempDir(), "data")
	if code := h.run("doctor", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	if c := find(decodeDoctor(t, h), "plugin shell"); c.Status != statusOK {
		t.Fatalf("sh on PATH should be ok, got %+v", c)
	}
}
