package external

import (
	"fmt"
	"sort"
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
	CategoryShellCompressor: {"noise_normalizer", "output_trimmer", "hook_large_output"},
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
	for _, b := range builtinOverlap[want.Category] {
		if contains(builtinsOn, b) {
			out = append(out, Conflict{want.Name, b, true, fmt.Sprintf(
				"%s overlaps the built-in %s, which is on. Turn the built-in off (set its state to off in your Trove config) at a session boundary first.",
				want.Name, b)})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Other < out[j].Other })
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
