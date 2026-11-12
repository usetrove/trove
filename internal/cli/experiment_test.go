package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A fresh install has no database. `experiment status` must report that there
// are no A/B results yet, not fail with a raw OS error, and must not create files.
func TestExperimentStatusFreshInstallIsNotAnError(t *testing.T) {
	h := newHarness(t, nil)
	dir := filepath.Join(t.TempDir(), "fresh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if code := h.run("experiment", "status", "--json", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, h.err)
	}
	var out struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &out); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, h.out)
	}
	if out.Results == nil || len(out.Results) != 0 {
		t.Errorf("want an empty results list, got %s", h.out)
	}
	if strings.Contains(h.err.String(), "GetFileAttributes") || strings.Contains(h.err.String(), "no such file") {
		t.Errorf("raw OS error leaked: %s", h.err)
	}
	if _, err := os.Stat(filepath.Join(dir, "trove.db")); !os.IsNotExist(err) {
		t.Errorf("experiment status must not create trove.db (stat err: %v)", err)
	}
}

func TestExperimentStatusHumanFreshInstall(t *testing.T) {
	h := newHarness(t, nil)
	dir := filepath.Join(t.TempDir(), "fresh")
	if code := h.run("experiment", "status", "--data-dir", dir); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, h.err)
	}
	if !strings.Contains(h.out.String(), "No optimizer has been in an experiment yet.") {
		t.Errorf("unexpected output: %s", h.out)
	}
}
