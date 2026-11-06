package recommend

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed defaults.toml
var embeddedDefaults []byte

// When is the condition under which a rule fires. Every field is optional and a
// zero value means "do not test". Minimums on session metrics are per-session
// means over the repo's recent sessions.
type When struct {
	TaskIn          []string `toml:"task_in"`           // task type is one of these
	SizeIn          []string `toml:"size_in"`           // repo size bucket is one of these
	TestFrameworkIn []string `toml:"test_framework_in"` // detected test framework is one of these
	LangIn          []string `toml:"lang_in"`           // one of the top languages is one of these

	MinSessions     int     `toml:"min_sessions"`
	MinFilesRead    float64 `toml:"min_files_read"`
	MinRepeatReads  float64 `toml:"min_repeat_reads"`
	MinCompactions  float64 `toml:"min_compactions"`
	MinRetries      float64 `toml:"min_retries"`
	MinToolResults  float64 `toml:"min_tool_results"`
	MinLargeResults float64 `toml:"min_large_results"`
	MinDupRatio     float64 `toml:"min_dup_ratio"`
	MinErrorShare   float64 `toml:"min_error_share"`
}

// usesSessions reports whether the rule looks at session metrics at all.
func (w When) usesSessions() bool {
	return w.MinSessions > 0 || w.MinFilesRead > 0 || w.MinRepeatReads > 0 || w.MinCompactions > 0 || w.MinRetries > 0 ||
		w.MinToolResults > 0 || w.MinLargeResults > 0 || w.MinDupRatio > 0 || w.MinErrorShare > 0
}

// usesRepo reports whether the rule looks at the repo's features.
func (w When) usesRepo() bool {
	return len(w.SizeIn) > 0 || len(w.TestFrameworkIn) > 0 || len(w.LangIn) > 0
}

// Rule is one row of the lookup table.
type Rule struct {
	ID     string `toml:"id"`
	Kind   Kind   `toml:"kind"`
	Name   string `toml:"name"`
	Action Action `toml:"action"`
	// Evidence is "none" until a reproducible benchmark run supports the rule, then
	// "benchmark" with Source naming the run.
	Evidence Evidence `toml:"evidence"`
	Source   string   `toml:"source"`
	// Reason is a template. Placeholders: {sessions} {files_read} {repeat_reads}
	// {large_results} {dup_ratio} {error_share} {compactions} {size} {framework} {task}.
	Reason string `toml:"reason"`
	When   When   `toml:"when"`
}

// Defaults is the bundled, versioned lookup table. It is replaced by newer
// benchmark results with each release (docs/recommend.md).
type Defaults struct {
	// Version is the schema version of the file.
	Version int `toml:"version"`
	// Release names the Trove release or benchmark bundle the rules came from,
	// so a report can say which defaults produced a suggestion.
	Release string `toml:"release"`
	// MinSessions is how many sessions of a repo a session-based rule needs
	// before it may fire at all.
	MinSessions int    `toml:"min_sessions"`
	Rules       []Rule `toml:"rule"`
}

// SupportedVersion is the newest schema this build reads.
const SupportedVersion = 1

// LoadDefaults parses and validates a defaults file. An empty input means no rules.
func LoadDefaults(b []byte) (Defaults, error) {
	var d Defaults
	if len(b) > 0 {
		if err := toml.Unmarshal(b, &d); err != nil {
			return Defaults{}, fmt.Errorf("the recommendation defaults file is not valid: %w", err)
		}
	}
	if d.Version > SupportedVersion {
		return Defaults{}, fmt.Errorf("the recommendation defaults file is version %d; this Trove reads up to version %d. Update Trove", d.Version, SupportedVersion)
	}
	if d.MinSessions < 1 {
		d.MinSessions = 3
	}
	seen := map[string]bool{}
	for _, r := range d.Rules {
		if err := r.validate(); err != nil {
			return Defaults{}, err
		}
		if seen[r.ID] {
			return Defaults{}, fmt.Errorf("the recommendation defaults file has two rules with the id %q", r.ID)
		}
		seen[r.ID] = true
	}
	return d, nil
}

func (r Rule) validate() error {
	bad := func(why string) error { return fmt.Errorf("recommendation rule %q: %s", r.ID, why) }
	switch {
	case r.ID == "":
		return fmt.Errorf("a recommendation rule has no id")
	case r.Name == "" || strings.TrimSpace(r.Reason) == "":
		return bad("needs a name and a reason")
	case r.Evidence != EvidenceNone && r.Evidence != EvidenceBenchmark:
		return bad(`evidence must be "none" or "benchmark"`)
	case r.Evidence == EvidenceBenchmark && r.Source == "":
		return bad(`evidence "benchmark" needs a source naming the benchmark run`)
	}
	switch r.Kind {
	case KindOptimizer:
		if r.Action != ActionShadow {
			// A rule may only suggest the safe first step. "enable" comes from the
			// user's own measured result, never from a lookup table (rule 6).
			return bad(`an optimizer rule may only suggest action "shadow"`)
		}
	case KindProfile:
		if r.Action != ActionUseProfile {
			return bad(`a profile rule must use action "use_profile"`)
		}
	default:
		return bad(`kind must be "optimizer" or "profile"`)
	}
	return nil
}

// BundledDefaults returns the defaults shipped in the binary. An unreadable
// embedded file means no rules, never wrong ones; a test keeps it readable.
func BundledDefaults() Defaults {
	d, err := LoadDefaults(embeddedDefaults)
	if err != nil {
		return Defaults{MinSessions: 3}
	}
	return d
}
