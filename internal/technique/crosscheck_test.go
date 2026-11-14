package technique_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/usetrove/trove/internal/autopilot"
	"github.com/usetrove/trove/internal/external"
	"github.com/usetrove/trove/internal/fixer"
	"github.com/usetrove/trove/internal/hooks"
	"github.com/usetrove/trove/internal/optimize"
	"github.com/usetrove/trove/internal/optimize/suggest"
	"github.com/usetrove/trove/internal/settings"
	"github.com/usetrove/trove/internal/technique"
)

// implemented lists every name that exists in the code today, from the code itself.
func implemented(t *testing.T) map[string]string {
	t.Helper()
	known := map[string]string{}
	add := func(src string, names ...string) {
		for _, n := range names {
			known[n] = src
		}
	}
	add("internal/optimize", optimize.PipelineNames...)
	add("internal/optimize (gateway)", optimize.GatewayNames...)
	add("internal/hooks", hooks.OptimizerNames...)
	add("internal/fixer", fixer.MCPPrune, fixer.ContextSlim, fixer.CacheBreaker, fixer.ModelDefault, fixer.SettingsCheck, fixer.TaskScout, fixer.TaskCascade)
	add("internal/optimize/suggest", suggest.KindCacheTTL)
	ms, err := external.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		add("internal/external", m.Name)
	}
	cat, err := settings.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range cat.Files {
		for _, s := range f.Settings {
			add("internal/settings catalog", s.Key)
		}
	}
	// Names for things that are commands or controllers, not optimizers: checked by the tests in their packages.
	add("command", "autopilot", "trove_mcp", "settings")
	return known
}

// A technique that is built, partly built or wrapped must be real; one that is
// still a candidate, a reference or excluded must not already exist under that
// name, because then its status is out of date.
func TestEveryNameMatchesTheCode(t *testing.T) {
	known := implemented(t)
	for _, x := range technique.All() {
		for _, n := range x.Names {
			_, real := known[n]
			switch x.Status {
			case technique.StatusBuilt, technique.StatusPartial, technique.StatusWrapped:
				if !real {
					t.Errorf("%s (%s) is %s but nothing in the code is called %q", x.ID, x.Title, x.Status, n)
				}
			case technique.StatusCandidate:
				if real {
					t.Errorf("%s is a candidate but %q already exists in %s: update its status", x.ID, n, known[n])
				}
			}
		}
	}
}

// Every optimizer the pipeline and the hooks know is in the registry, so none can
// be turned on without its conflicts and overhead being on record.
func TestEveryOptimizerIsRegistered(t *testing.T) {
	for _, n := range append(append(append([]string{}, optimize.PipelineNames...), optimize.GatewayNames...), hooks.OptimizerNames...) {
		if _, ok := technique.Get(n); !ok {
			t.Errorf("optimizer %q is not in the technique registry", n)
		}
	}
	for _, k := range []string{fixer.MCPPrune, fixer.ContextSlim, fixer.CacheBreaker, fixer.ModelDefault, fixer.SettingsCheck} {
		if _, ok := technique.Get(k); !ok {
			t.Errorf("fix kind %q is not in the technique registry", k)
		}
	}
	ms, err := external.Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if _, ok := technique.Get(m.Name); !ok {
			t.Errorf("wrapped tool %q is not in the technique registry", m.Name)
		}
	}
}

// Autopilot may only promote what the registry marks as automatic.
func TestAutopilotEligibilityComesFromTheRegistry(t *testing.T) {
	for _, n := range autopilot.DefaultPolicy().Eligible {
		x, ok := technique.Get(n)
		if !ok || !x.Auto {
			t.Errorf("Autopilot may promote %q but the registry does not mark it automatic (found: %v)", n, ok)
		}
	}
	// And the other way: nothing is marked automatic that Autopilot would not promote, so the two lists agree.
	elig := map[string]bool{}
	for _, n := range autopilot.DefaultPolicy().Eligible {
		elig[n] = true
	}
	for _, x := range technique.All() {
		if !x.Auto {
			continue
		}
		for _, n := range x.Names {
			if !elig[n] {
				t.Errorf("%s (%s) is marked automatic but is not in Autopilot's eligible list", x.ID, n)
			}
		}
	}
}

// The tables in docs/techniques.md and the registry list the same IDs.
func TestRegistryMatchesTheDocument(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "techniques.md"))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\| ([BCW][0-9]+) \|`)
	inDoc := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(b), -1) {
		inDoc[m[1]] = true
	}
	inCode := map[string]bool{}
	for _, x := range technique.All() {
		inCode[x.ID] = true
	}
	var missing, extra []string
	for id := range inCode {
		if !inDoc[id] {
			missing = append(missing, id)
		}
	}
	for id := range inDoc {
		if !inCode[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing) > 0 {
		t.Errorf("in the registry but not in docs/techniques.md: %v", missing)
	}
	if len(extra) > 0 {
		t.Errorf("in docs/techniques.md but not in the registry: %v", extra)
	}
}

// The registry's hook capability needs must agree with what the hooks really do: an optimizer that
// rewrites a tool's input needs an input capability, one that rewrites output needs an output
// capability, and one that rewrites nothing (denies a read, or only advises) needs none.
func TestHookNeedsMatchWhatTheHooksRewrite(t *testing.T) {
	for _, n := range hooks.OptimizerNames {
		x, ok := technique.Get(n)
		if !ok {
			t.Errorf("%s is not in the registry", n)
			continue
		}
		kind := hooks.RewriteKindOf(n)
		var kinds []string
		for _, need := range x.HookNeeds {
			_, k, _ := strings.Cut(need, ":")
			kinds = append(kinds, k)
		}
		switch {
		case kind == "" && len(kinds) > 0:
			t.Errorf("%s rewrites nothing but the registry says it needs %v", n, x.HookNeeds)
		case kind != "" && len(kinds) == 0:
			t.Errorf("%s does a %s rewrite but the registry lists no hook capability for it", n, kind)
		}
		for _, k := range kinds {
			if k != kind {
				t.Errorf("%s: the hooks do a %q rewrite but the registry needs a %q capability", n, kind, k)
			}
		}
	}
}
