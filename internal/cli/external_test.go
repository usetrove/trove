package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/external"
)

func withManifests(t *testing.T, ms ...external.Manifest) {
	t.Helper()
	externalManifests = func() ([]external.Manifest, error) { return ms, nil }
	t.Cleanup(func() { externalManifests = external.Builtin })
}

func fixtureTool() external.Manifest {
	return external.Manifest{Name: "sq", Version: "2.0.0", License: "Elastic-2.0", SourceURL: "https://example.com/sq",
		VerifiedOn: "2026-11-04", ActsAs: external.ActsShellWrapper, Category: external.CategoryShellCompressor,
		Points:  []string{"tool_output"},
		Install: external.Command{Argv: []string{"trove-test-no-such-program", "install"}},
		Claimed: external.Claim{Text: "claims 60-90%", SourceURL: "https://example.com/sq"}}
}

func TestExternalListAndShowJSON(t *testing.T) {
	withManifests(t, fixtureTool())
	h := newHarness(t, nil)
	if code := h.run("--json", "external", "list"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	var got struct {
		Tools []externalRow `json:"tools"`
	}
	if err := json.Unmarshal(h.out.Bytes(), &got); err != nil || len(got.Tools) != 1 {
		t.Fatalf("bad json %v: %s", err, h.out)
	}
	if got.Tools[0].State != "not installed" || got.Tools[0].LicenseClass != "restricted" {
		t.Errorf("row = %+v", got.Tools[0])
	}
	h.out.Reset()
	if code := h.run("external", "show", "sq"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	for _, want := range []string{"WARNING", "restricts commercial", "claims 60-90%", "cannot run in shadow mode", "trove-test-no-such-program install"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("show output lacks %q:\n%s", want, h.out)
		}
	}
}

// Installing runs a program, so piped "y" must never be enough.
func TestExternalInstallRefusesWithoutATerminal(t *testing.T) {
	withManifests(t, fixtureTool())
	h := newHarness(t, nil)
	h.app.In = strings.NewReader("y\nsq\n")
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = defaultStdinIsTerminal })
	if code := h.run("external", "install", "sq"); code == 0 {
		t.Fatal("installed without a terminal")
	}
	if !strings.Contains(h.err.String(), "person at a terminal") {
		t.Errorf("unhelpful error: %s", h.err)
	}
}

func TestExternalInstallDeclinedChangesNothing(t *testing.T) {
	withManifests(t, fixtureTool())
	h := newHarness(t, nil)
	h.app.In = strings.NewReader("n\n")
	stdinIsTerminal = func() bool { return true }
	t.Cleanup(func() { stdinIsTerminal = defaultStdinIsTerminal })
	if code := h.run("external", "install", "sq"); code != 0 {
		t.Fatalf("exit %d: %s", code, h.err)
	}
	h.out.Reset()
	h.app.In = nil
	_ = h.run("--json", "external", "status")
	if strings.Contains(h.out.String(), `"installed": true`) {
		t.Fatalf("declined install was recorded:\n%s", h.out)
	}
}

func TestExternalUnknownTool(t *testing.T) {
	withManifests(t)
	h := newHarness(t, nil)
	if code := h.run("external", "show", "nope"); code == 0 || !strings.Contains(h.err.String(), "trove external list") {
		t.Fatalf("code %d err %s", code, h.err)
	}
}
