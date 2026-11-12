package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/recovery"
)

// A tampered stored output must reach the model as an error that says the output is unavailable. It must not
// come back as an empty success, which the model would read as "the output was empty".
func TestFetchReportsTamperedOutputAsUnavailable(t *testing.T) {
	dir := t.TempDir()
	st, err := recovery.Open(dir, recovery.Options{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join("..", "recovery", "testdata", "tampered", "527452153c96f0ac.out"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "527452153c96f0ac.out"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	text, err := Fetch(st, FetchArgs{OutputID: "527452153c96f0ac"})
	if err == nil {
		t.Fatalf("a tampered output was returned as a success with %d bytes", len(text))
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("the error does not tell the model the output is unavailable: %q", err)
	}

	res, _, _ := reply(text, err)
	if !res.IsError {
		t.Fatal("the model would receive this as a success, not an error")
	}
}
