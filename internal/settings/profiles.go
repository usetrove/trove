package settings

import (
	_ "embed"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed profiles.toml
var profilesTOML []byte

// ProfilesVersion is the schema version of profiles.toml.
const ProfilesVersion = 1

// EvidenceNone marks a value no benchmark or A/B result supports yet.
const EvidenceNone = "none"

// Value is one setting a profile sets for one harness.
type Value struct {
	Harness string   `toml:"harness" json:"harness"`
	Key     string   `toml:"key" json:"key"`
	Value   string   `toml:"value" json:"value,omitempty"` // int, enum and string settings
	List    []string `toml:"list" json:"list,omitempty"`   // string_list settings
	// Evidence is "none" or a reference such as "bench:2026-12-01-quickfix" or
	// "ab:<id>". It is a pointer to a result, never a claim by itself.
	Evidence string `toml:"evidence" json:"evidence"`
	// Regressed is set when a benchmark or A/B showed worse task success.
	// Such a value must not ship.
	Regressed bool   `toml:"regressed" json:"regressed,omitempty"`
	Why       string `toml:"why" json:"why"`
}

// Profile is the set of values for one task type.
type Profile struct {
	Task        string  `toml:"task" json:"task"`
	Description string  `toml:"description" json:"description"`
	Values      []Value `toml:"value" json:"values"`
}

// ProfileSet is the loaded profiles.toml.
type ProfileSet struct {
	Version  int       `toml:"version" json:"version"`
	Profiles []Profile `toml:"profile" json:"profiles"`
}

var taskRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ParseProfiles decodes profile data. It does not check against a catalog;
// call Validate for that.
func ParseProfiles(data []byte) (*ProfileSet, error) {
	var p ProfileSet
	dec := toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("could not read settings profiles: %w", err)
	}
	if p.Version != ProfilesVersion {
		return nil, fmt.Errorf("settings profiles are version %d; this Trove reads version %d. Upgrade Trove", p.Version, ProfilesVersion)
	}
	return &p, nil
}

// BuiltinProfiles loads the embedded profiles and validates them against the
// embedded catalog.
func BuiltinProfiles(c *Catalog) (*ProfileSet, error) {
	p, err := ParseProfiles(profilesTOML)
	if err != nil {
		return nil, err
	}
	return p, p.Validate(c)
}

// Tasks lists the task types, sorted.
func (p *ProfileSet) Tasks() []string {
	var out []string
	for _, pr := range p.Profiles {
		out = append(out, pr.Task)
	}
	sort.Strings(out)
	return out
}

// Get returns the profile for a task type.
func (p *ProfileSet) Get(task string) (Profile, bool) {
	for _, pr := range p.Profiles {
		if pr.Task == task {
			return pr, true
		}
	}
	return Profile{}, false
}

// Validate checks every value against the catalog. A shipped profile may only
// contain settings Trove is allowed to write, with values the docs confirmed,
// and nothing known to hurt task success.
func (p *ProfileSet) Validate(c *Catalog) error {
	seenTask := map[string]bool{}
	for _, pr := range p.Profiles {
		if !taskRE.MatchString(pr.Task) {
			return fmt.Errorf("profile task %q must be lowercase letters, digits and -", pr.Task)
		}
		if seenTask[pr.Task] {
			return fmt.Errorf("profile %s appears twice", pr.Task)
		}
		seenTask[pr.Task] = true
		seen := map[string]bool{}
		for _, v := range pr.Values {
			if err := v.Validate(c); err != nil {
				return fmt.Errorf("profile %s: %w", pr.Task, err)
			}
			id := v.Harness + "|" + v.Key
			if seen[id] {
				return fmt.Errorf("profile %s sets %s twice", pr.Task, v.Key)
			}
			seen[id] = true
		}
	}
	return nil
}

// Validate checks one value.
func (v Value) Validate(c *Catalog) error {
	s, ok := c.Lookup(v.Harness, v.Key)
	if !ok {
		return fmt.Errorf("%s is not in the %s settings catalog", v.Key, v.Harness)
	}
	if !s.Applyable {
		return fmt.Errorf("%s cannot be applied by Trove (it is suggestion-only until its documentation is confirmed)", v.Key)
	}
	if v.Evidence == "" {
		return fmt.Errorf("%s needs an evidence field (use %q if there is none)", v.Key, EvidenceNone)
	}
	if v.Why == "" {
		return fmt.Errorf("%s needs a why", v.Key)
	}
	if v.Regressed {
		return fmt.Errorf("%s is marked as hurting task success and must not ship", v.Key)
	}
	return s.CheckValue(v.Value, v.List)
}

// CheckValue reports whether value (or list, for string_list settings) is one
// Trove may write for this setting.
func (s Setting) CheckValue(value string, list []string) error {
	switch s.Type {
	case "string_list":
		if len(list) == 0 || value != "" {
			return fmt.Errorf("%s takes a list", s.Key)
		}
		return nil
	}
	if len(list) > 0 {
		return fmt.Errorf("%s takes a single value", s.Key)
	}
	if value == "" {
		return fmt.Errorf("%s needs a value", s.Key)
	}
	if len(s.VerifiedValues) > 0 && !contains(s.VerifiedValues, value) {
		return fmt.Errorf("%s=%s has not been confirmed in the documentation (confirmed: %s)", s.Key, value, strings.Join(s.VerifiedValues, ", "))
	}
	switch s.Type {
	case "int":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return fmt.Errorf("%s=%q is not a whole number", s.Key, value)
		}
		if s.Min != nil && n < *s.Min || s.Max != nil && n > *s.Max {
			return fmt.Errorf("%s=%d is outside the documented range", s.Key, n)
		}
	case "enum":
		if !contains(s.Enum, value) {
			return fmt.Errorf("%s=%s is not one of %s", s.Key, value, strings.Join(s.Enum, ", "))
		}
	case "bool":
		if value != "true" && value != "false" {
			return fmt.Errorf("%s must be true or false", s.Key)
		}
	}
	if s.Pattern != "" {
		if ok, _ := regexp.MatchString(s.Pattern, value); !ok {
			return fmt.Errorf("%s=%q does not match the documented form", s.Key, value)
		}
	}
	return nil
}

// Unproven reports whether applying v would change behavior without evidence
// that it is safe: the setting can lower capability or change behavior and no
// benchmark or A/B reference backs the value.
func (v Value) Unproven(c *Catalog) bool {
	s, ok := c.Lookup(v.Harness, v.Key)
	if !ok {
		return true
	}
	return v.Evidence == EvidenceNone && s.Risk != RiskNone
}

// Selection is what a profile resolves to for one harness.
type Selection struct {
	Use     []Value        `json:"use"`
	Skipped []SkippedValue `json:"skipped,omitempty"`
}

// SkippedValue is a value left out, with the plain-English reason.
type SkippedValue struct {
	Value  Value  `json:"value"`
	Reason string `json:"reason"`
}

// Select picks the values of a profile for one harness. Unproven values are
// left out unless allowUnproven is set. Output order is the profile's order,
// so the same inputs always give the same plan.
func (p Profile) Select(c *Catalog, harness string, allowUnproven bool) Selection {
	var sel Selection
	for _, v := range p.Values {
		if v.Harness != harness {
			continue
		}
		if v.Unproven(c) && !allowUnproven {
			sel.Skipped = append(sel.Skipped, SkippedValue{v, "no benchmark or A/B evidence yet that this is safe; set settings.allow_unproven = true to include it"})
			continue
		}
		sel.Use = append(sel.Use, v)
	}
	return sel
}
