package external

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/technique"
)

// The registry answers the built-in overlap question for every shipped tool, and
// for the categories that had a list before the registry it gives the same list.
// This keeps `trove external enable` refusing exactly what it refused before.
func TestRegistryAnswersTheSameOverlapAsTheCategoryTable(t *testing.T) {
	ms, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		x, ok := technique.Get(m.Name)
		if !ok {
			t.Errorf("%s is shipped but not in the technique registry", m.Name)
			continue
		}
		if x.Status != technique.StatusWrapped {
			t.Errorf("%s: registry status %q, want wrapped", m.Name, x.Status)
		}
		// The category the manifest declares is the resource the registry says it claims.
		claimed := false
		for _, c := range x.Claims {
			claimed = claimed || c.Resource == string(m.Category)
		}
		if !claimed {
			t.Errorf("%s: manifest category %q is not among the registry's claims %+v", m.Name, m.Category, x.Claims)
		}
		want := append([]string{}, builtinOverlap[m.Category]...)
		sort.Strings(want)
		got := technique.OverlappingBuiltins(m.Name)
		if len(want) > 0 && !reflect.DeepEqual(got, want) {
			t.Errorf("%s: registry says it overlaps %v, the category table says %v", m.Name, got, want)
		}
		if len(want) == 0 {
			// With no built-in optimizer of that kind, the registry may name only the MCP tools (a code-navigation tool).
			for _, g := range got {
				if g != "trove_mcp" {
					t.Errorf("%s: the registry names an overlap %q the category table does not", m.Name, g)
				}
			}
		}
		// A manifest and the registry agree on the licence family, so a licence change is noticed in both.
		if m.License != "" && !strings.Contains(strings.ToLower(x.Licence), strings.ToLower(strings.SplitN(m.License, " ", 2)[0])) && x.Licence != "Trove project" {
			t.Errorf("%s: manifest licence %q does not appear in the registry licence %q", m.Name, m.License, x.Licence)
		}
	}
}

// Enabling a shell compressor beside the built-in output optimizers is refused, with the same text as before.
func TestEnableRefusesOverlapUsingTheRegistry(t *testing.T) {
	ms, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	var rtk Manifest
	for _, m := range ms {
		if m.Name == "rtk" {
			rtk = m
		}
	}
	if rtk.Name == "" {
		t.Skip("no rtk manifest")
	}
	got := CheckEnable(rtk, nil, []string{"noise_normalizer", "hook_repeat_read"})
	if len(got) != 1 || !got[0].Builtin || got[0].Other != "noise_normalizer" || !strings.Contains(got[0].Reason, "overlaps the built-in noise_normalizer") {
		t.Fatalf("got %+v", got)
	}
	if got := CheckEnable(rtk, nil, []string{"hook_repeat_read", "hook_quiet_commands"}); len(got) != 0 {
		t.Errorf("refused over a built-in it never refused before: %+v", got)
	}
}
