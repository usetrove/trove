package cli

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/pricing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestPricingShowGolden(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("pricing", "show"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	golden := filepath.Join("testdata", "pricing_show.golden")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, h.out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/cli -update to create it)", err)
	}
	if got := strings.ReplaceAll(h.out.String(), "\r\n", "\n"); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("pricing show output changed; if intended run go test ./internal/cli -update\n%s", got)
	}
}

func TestPricingShowShowsVerifiedDatesAndLabels(t *testing.T) {
	h := newHarness(t, nil)
	h.run("pricing", "show")
	out := h.out.String()
	for _, want := range []string{"VERIFIED", "claude-sonnet-5-5", "gpt-5.5", "gemini-2.5-pro", "2026-11-12", "estimated", pricing.DataVersion} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q", want)
		}
	}
}

func TestPricingShowJSONAndFilter(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("pricing", "show", "--json", "--provider", "openai"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var got struct {
		Version string `json:"pricing_data_version"`
		Basis   string `json:"basis"`
		Entries []struct {
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			SourceURL  string `json:"source_url"`
			VerifiedAt string `json:"verified_at"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil {
		t.Fatalf("bad json: %v\n%s", err, h.out)
	}
	if got.Version != pricing.DataVersion || !strings.HasPrefix(got.Basis, "estimated") || len(got.Entries) == 0 {
		t.Fatalf("unexpected json header: %+v", got)
	}
	for _, e := range got.Entries {
		if e.Provider != "openai" || e.SourceURL == "" || e.VerifiedAt == "" {
			t.Errorf("bad entry %+v", e)
		}
	}
}

func TestPricingShowUnknownProviderFails(t *testing.T) {
	h := newHarness(t, nil)
	if code := h.run("pricing", "show", "--provider", "acme"); code != 1 || !strings.Contains(h.err.String(), "anthropic, openai or google") {
		t.Errorf("exit %d stderr %q", code, h.err)
	}
}
