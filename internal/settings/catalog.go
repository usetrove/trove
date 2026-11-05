// Package settings tunes each harness's built-in token controls (thinking
// budget, effort, compaction, MCP limits, context exclusions, Codex profiles)
// per project and task type. The list of settings is data (embedded, versioned
// catalog files), not logic: harness settings change often, and every entry
// records where and when it was verified against official documentation.
package settings

import (
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

//go:embed catalog/*.toml
var catalogFS embed.FS

// CatalogVersion is the schema version of the catalog files.
const CatalogVersion = 1

// Harness names match fixer.Fix.Agent.
const (
	ClaudeCode = "claude_code"
	Codex      = "codex"
)

// Target says where a setting is written.
type Target string

// Where a setting is written.
const (
	TargetSettingsJSON Target = "settings_json" // top-level key in Claude Code settings.json
	TargetSettingsEnv  Target = "settings_env"  // key inside the "env" block of settings.json
	TargetShellEnv     Target = "shell_env"     // only documented as a shell variable; Trove suggests, never writes
	TargetMCPServer    Target = "mcp_server"    // per-server key in an MCP config entry
	TargetCodexProfile Target = "codex_profile" // top-level key in ~/.codex/<name>.config.toml
)

// Verified says how well an entry is confirmed.
//   - verbatim: the key and its values were read as raw text on the official page.
//   - extractor: seen only in a model-extracted view of the page; usable for
//     display, never for writing a file.
type Verified string

// How well an entry is confirmed.
const (
	VerifiedVerbatim  Verified = "verbatim"
	VerifiedExtractor Verified = "extractor"
)

// Risk says whether lowering the value can cost capability.
type Risk string

// Whether lowering the value can cost capability.
const (
	RiskNone             Risk = "none"
	RiskChangesBehavior  Risk = "changes_behavior"
	RiskLowersCapability Risk = "lowers_capability"
)

// Setting is one catalog entry.
type Setting struct {
	Key         string   `toml:"key" json:"key"`
	Harness     string   `toml:"harness" json:"harness"`
	Target      Target   `toml:"target" json:"target"`
	Scopes      []string `toml:"scopes" json:"scopes"` // user, project, local, managed, env, cli
	Type        string   `toml:"type" json:"type"`     // int, enum, bool, string, string_list
	Enum        []string `toml:"enum" json:"enum,omitempty"`
	Pattern     string   `toml:"pattern" json:"pattern,omitempty"` // string values must match
	Min         *int64   `toml:"min" json:"min,omitempty"`
	Max         *int64   `toml:"max" json:"max,omitempty"`
	Default     string   `toml:"default" json:"default,omitempty"` // "" = unset / not stated
	DefaultNote string   `toml:"default_note" json:"default_note,omitempty"`
	Since       string   `toml:"since" json:"since,omitempty"` // first harness version; "" = not stated
	Until       string   `toml:"until" json:"until,omitempty"` // first version where it no longer works
	DocURL      string   `toml:"doc_url" json:"doc_url"`
	VerifiedOn  string   `toml:"verified_on" json:"verified_on"` // YYYY-MM-DD
	Verified    Verified `toml:"verified" json:"verified"`
	// VerifiedValues are values seen verbatim in the docs; a profile may only
	// write these for settings Trove applies.
	VerifiedValues []string `toml:"verified_values" json:"verified_values,omitempty"`
	Risk           Risk     `toml:"risk" json:"risk"`
	// Applyable is true only when Trove may write the setting through the Fixer.
	Applyable bool   `toml:"applyable" json:"applyable"`
	Notes     string `toml:"notes" json:"notes,omitempty"`
}

// Gap is something the task asked for that official docs did not confirm.
type Gap struct {
	Key    string `toml:"key" json:"key"`
	Reason string `toml:"reason" json:"reason"`
}

// File is one embedded catalog file.
type File struct {
	Version     int       `toml:"version" json:"version"`
	Harness     string    `toml:"harness" json:"harness"`
	CheckedOn   string    `toml:"checked_on" json:"checked_on"`
	DocsVersion string    `toml:"docs_version" json:"docs_version,omitempty"` // newest harness version seen in the docs
	Settings    []Setting `toml:"setting" json:"settings"`
	Gaps        []Gap     `toml:"gap" json:"gaps,omitempty"`
}

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Validate rejects entries Trove must not act on.
func (s Setting) Validate() error {
	var bad []string
	add := func(ok bool, msg string) {
		if !ok {
			bad = append(bad, msg)
		}
	}
	add(s.Key != "", "key is required")
	add(s.Harness == ClaudeCode || s.Harness == Codex, "harness must be claude_code or codex")
	switch s.Target {
	case TargetSettingsJSON, TargetSettingsEnv, TargetShellEnv, TargetMCPServer, TargetCodexProfile:
	default:
		bad = append(bad, "unknown target")
	}
	add((s.Harness == Codex) == (s.Target == TargetCodexProfile), "codex_profile target is for codex settings only")
	add(len(s.Scopes) > 0, "scopes are required")
	switch s.Type {
	case "int":
		add(len(s.Enum) == 0, "int settings must not list enum values")
		if s.Min != nil && s.Max != nil {
			add(*s.Min <= *s.Max, "min must not exceed max")
		}
	case "enum":
		add(len(s.Enum) > 0, "enum settings must list values")
	case "bool", "string", "string_list":
	default:
		bad = append(bad, "type must be int, enum, bool, string or string_list")
	}
	if s.Pattern != "" {
		if _, err := regexp.Compile(s.Pattern); err != nil {
			bad = append(bad, "pattern does not compile")
		}
	}
	add(strings.HasPrefix(s.DocURL, "https://"), "doc_url must be https")
	add(dateRE.MatchString(s.VerifiedOn), "verified_on must be YYYY-MM-DD")
	add(s.Verified == VerifiedVerbatim || s.Verified == VerifiedExtractor, "verified must be verbatim or extractor")
	switch s.Risk {
	case RiskNone, RiskChangesBehavior, RiskLowersCapability:
	default:
		bad = append(bad, "risk must be none, changes_behavior or lowers_capability")
	}
	if s.Since != "" {
		_, err := ParseVersion(s.Since)
		add(err == nil, "since is not a version")
	}
	if s.Until != "" {
		_, err := ParseVersion(s.Until)
		add(err == nil, "until is not a version")
	}
	// Rule 8: never write a setting that was not read from the docs as raw text.
	if s.Applyable {
		add(s.Verified == VerifiedVerbatim, "applyable settings must be verified verbatim")
		add(s.Target != TargetShellEnv, "shell-only settings cannot be applyable")
		if s.Type == "enum" || s.Type == "string" {
			add(len(s.VerifiedValues) > 0 || s.Type == "string" && s.Pattern != "",
				"applyable settings need verified_values (or a pattern)")
		}
	}
	for _, v := range s.VerifiedValues {
		if s.Type == "enum" {
			add(contains(s.Enum, v), "verified value "+v+" is not in enum")
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("setting %s/%s is invalid: %s", s.Harness, s.Key, strings.Join(bad, "; "))
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Validate checks a whole file.
func (f File) Validate() error {
	if f.Version != CatalogVersion {
		return fmt.Errorf("settings catalog is version %d; this Trove reads version %d. Upgrade Trove", f.Version, CatalogVersion)
	}
	if f.Harness != ClaudeCode && f.Harness != Codex {
		return fmt.Errorf("settings catalog names unknown harness %q", f.Harness)
	}
	if !dateRE.MatchString(f.CheckedOn) {
		return fmt.Errorf("settings catalog for %s needs checked_on as YYYY-MM-DD", f.Harness)
	}
	seen := map[string]bool{}
	for _, s := range f.Settings {
		if err := s.Validate(); err != nil {
			return err
		}
		if s.Harness != f.Harness {
			return fmt.Errorf("setting %s is in the %s catalog but names harness %s", s.Key, f.Harness, s.Harness)
		}
		id := s.Key + "|" + string(s.Target)
		if seen[id] {
			return fmt.Errorf("setting %s appears twice for target %s", s.Key, s.Target)
		}
		seen[id] = true
	}
	return nil
}

// Parse decodes and validates one catalog file. Unknown fields are errors so
// a typo cannot silently drop a safety field.
func Parse(data []byte) (File, error) {
	var f File
	dec := toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("could not read settings catalog: %w", err)
	}
	return f, f.Validate()
}

// Catalog is the loaded set of settings for all harnesses.
type Catalog struct {
	Files []File `json:"files"`
}

// Builtin loads the embedded catalogs, sorted by harness.
func Builtin() (*Catalog, error) {
	ents, err := catalogFS.ReadDir("catalog")
	if err != nil {
		return nil, err
	}
	c := &Catalog{}
	seen := map[string]bool{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		data, err := catalogFS.ReadFile("catalog/" + e.Name())
		if err != nil {
			return nil, err
		}
		f, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if seen[f.Harness] {
			return nil, fmt.Errorf("two settings catalogs name harness %s", f.Harness)
		}
		seen[f.Harness] = true
		c.Files = append(c.Files, f)
	}
	sort.Slice(c.Files, func(i, j int) bool { return c.Files[i].Harness < c.Files[j].Harness })
	return c, nil
}

// File returns the catalog for one harness.
func (c *Catalog) File(harness string) (File, bool) {
	for _, f := range c.Files {
		if f.Harness == harness {
			return f, true
		}
	}
	return File{}, false
}

// Lookup finds a setting by key and target.
func (c *Catalog) Lookup(harness, key string) (Setting, bool) {
	f, ok := c.File(harness)
	if !ok {
		return Setting{}, false
	}
	for _, s := range f.Settings {
		if s.Key == key {
			return s, true
		}
	}
	return Setting{}, false
}
