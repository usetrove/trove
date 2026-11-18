package external

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/usetrove/trove/internal/technique"
)

// exclusive categories allow at most one enabled tool.
var exclusive = map[Category]bool{
	CategoryShellCompressor: true,
	CategoryCodeNav:         true,
	CategoryBehaviorRules:   true,
}

// builtinOverlap maps a category to built-in optimizers that do the same job.
// Enabling both double-trims the same output, so it is refused as well.
var builtinOverlap = map[Category][]string{
	CategoryShellCompressor: {"noise_normalizer", "output_trimmer", "hook_large_output", "hook_quiet_commands"},
	// verbosity_steer asks for shorter replies at the gateway: a second output-style rule set (plan 6.3).
	CategoryBehaviorRules: {"verbosity_steer"},
}

// Conflict explains why a tool cannot be enabled.
type Conflict struct {
	Tool    string `json:"tool"`
	Other   string `json:"other"`
	Builtin bool   `json:"builtin"`
	Reason  string `json:"reason"`
}

func (c Conflict) Error() string { return c.Reason }

// CheckEnable returns the conflicts that stop `want` from being enabled given
// the manifests of other enabled external tools and the built-ins that are on.
// Both lists may contain want itself; it is ignored.
func CheckEnable(want Manifest, enabled []Manifest, builtinsOn []string) []Conflict {
	var out []Conflict
	for _, o := range enabled {
		if o.Name == want.Name {
			continue
		}
		switch {
		case contains(want.ConflictsWith, o.Name) || contains(o.ConflictsWith, want.Name):
			out = append(out, Conflict{want.Name, o.Name, false, fmt.Sprintf(
				"%s cannot run together with %s. Run `trove external disable %s` first.", want.Name, o.Name, o.Name)})
		case want.Category == o.Category && exclusive[want.Category]:
			out = append(out, Conflict{want.Name, o.Name, false, fmt.Sprintf(
				"%s and %s are both %s tools and would work on the same output. Run `trove external disable %s` first.",
				want.Name, o.Name, want.Category, o.Name)})
		}
	}
	for _, b := range overlapFor(want) {
		if contains(builtinsOn, b) {
			out = append(out, Conflict{want.Name, b, true, fmt.Sprintf(
				"%s overlaps the built-in %s, which is on. Turn the built-in off (set its state to off in your Trove config) at a session boundary first.",
				want.Name, b)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Other < out[j].Other })
	return out
}

// HandWired returns a conflict for each other shell compressor whose Bash hook is
// already in the agent's settings.json, for example after `rtk init` or `ztk init -g`
// run by hand. Such a hook owns the Bash call just as a tool Trove enabled does, so a
// second owner is refused. Tools in others that are already reported (enabled through
// Trove) are skipped. An unreadable or missing file yields nothing: it cannot hold a hook.
func HandWired(want Manifest, all []Manifest, settingsPath string, reported []Conflict) []Conflict {
	if want.Category != CategoryShellCompressor || settingsPath == "" {
		return nil
	}
	b, err := os.ReadFile(settingsPath) //nolint:gosec // user's own agent config
	if err != nil {
		return nil
	}
	var doc struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return nil
	}
	done := map[string]bool{}
	for _, c := range reported {
		done[c.Other] = true
	}
	var out []Conflict
	for _, o := range all {
		if o.Name == want.Name || o.Category != CategoryShellCompressor || o.ActsAs != ActsHook || done[o.Name] {
			continue
		}
		bin := strings.Fields(o.Wire.HookCommand)[0]
		for _, g := range doc.Hooks["PreToolUse"] {
			if g.Matcher != "" && g.Matcher != "*" && !strings.Contains(g.Matcher, "Bash") {
				continue
			}
			for _, h := range g.Hooks {
				if f := strings.Fields(h.Command); len(f) > 0 && (f[0] == bin || strings.HasSuffix(f[0], "/"+bin)) && !done[o.Name] {
					done[o.Name] = true
					out = append(out, Conflict{want.Name, o.Name, false, fmt.Sprintf(
						"%s's Bash hook (%q) is already in %s, so it owns every Bash call. Remove that hook entry first, or run `trove external disable %s` if Trove added it.",
						o.Name, h.Command, settingsPath, o.Name)})
				}
			}
		}
	}
	return out
}

// overlapFor returns the built-in optimizers that do the same job as the tool.
// The technique registry is the source: a tool it knows is answered from there, so
// the list lives in one place. A tool the registry does not know yet falls back to
// the category table above.
func overlapFor(m Manifest) []string {
	if _, ok := technique.Get(m.Name); ok {
		return technique.OverlappingBuiltins(m.Name)
	}
	return builtinOverlap[m.Category]
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
