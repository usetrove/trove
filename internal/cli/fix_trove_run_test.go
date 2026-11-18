package cli

import (
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/fixer"
)

func TestFixListShowsTroveRunAdoption(t *testing.T) {
	r := fixListReport{Project: "/p", TroveRun: &fixer.RunAdoption{Commands: 2, AdoptedSessions: 3, HookSessions: 7, Label: "measured", Observable: true}}
	if got := renderFixList(r); !strings.Contains(got, "trove_run used in 3 of 7 sessions since 2 retyped command(s) were added (measured") {
		t.Fatalf("missing adoption line:\n%s", got)
	}
	r.TroveRun = &fixer.RunAdoption{Commands: 1, Label: "measured"}
	if got := renderFixList(r); !strings.Contains(got, "do not count use of trove_run") || strings.Contains(got, "used in") {
		t.Fatalf("unobservable adoption must not show a number:\n%s", got)
	}
	r.TroveRun = nil
	if strings.Contains(renderFixList(r), "trove_run") {
		t.Fatal("no retyped fixes, no line")
	}
}
