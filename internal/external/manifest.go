// Package external wraps third-party token tools as Trove optimizers so they
// can be evaluated with the same states, guardrails, overhead accounting and
// report labels as built-ins. Trove never installs a tool without explicit,
// interactive approval, and never loads manifests from untrusted locations:
// manifests are embedded in the binary.
package external

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/usetrove/trove/internal/optimize"
)

//go:embed manifests
var manifestFS embed.FS

// ActsAs describes how a tool reaches the agent.
type ActsAs string

const (
	ActsShellWrapper ActsAs = "shell_wrapper"
	ActsMCPServer    ActsAs = "mcp_server"
	ActsHook         ActsAs = "hook"
	ActsRulesFile    ActsAs = "rules_file"
)

// Category groups tools that do the same job. At most one tool per exclusive
// category may be enabled at a time.
type Category string

const (
	CategoryShellCompressor Category = "shell_compressor"
	CategoryCodeNav         Category = "code_nav"
	CategoryBehaviorRules   Category = "behavior_rules"
)

// Command is an argv array. It is never passed through a shell.
type Command struct {
	Argv     []string `toml:"argv" json:"argv"`
	Requires []string `toml:"requires" json:"requires,omitempty"` // binaries that must be on PATH first
}

// Verify is run after install to confirm the pinned version is what landed.
type Verify struct {
	Argv     []string `toml:"argv" json:"argv"`
	Contains string   `toml:"contains" json:"contains"` // must appear in the output
}

// Wire says how an enabled tool is connected to the agent.
//   - hook tools: Trove adds one PreToolUse-style hook entry to the agent's
//     settings through its own backed-up, undoable edit.
//   - MCP-server tools: Trove runs the agent's own add command, because the
//     agent keeps that registration in a file Trove does not edit. Remove undoes it.
type Wire struct {
	HookEvent   string   `toml:"hook_event" json:"hook_event,omitempty"`
	HookMatcher string   `toml:"hook_matcher" json:"hook_matcher,omitempty"`
	HookCommand string   `toml:"hook_command" json:"hook_command,omitempty"`
	Add         []string `toml:"add" json:"add,omitempty"`
	Remove      []string `toml:"remove" json:"remove,omitempty"`
}

// Overhead is what the tool adds to every request while on (rule 15).
type Overhead struct {
	ToolDefTokens  int `toml:"tool_def_tokens" json:"tool_def_tokens"`
	InjectedTokens int `toml:"injected_tokens" json:"injected_tokens"`
	// ExtraTurnsEstimate is a declared guess, always shown as estimated.
	ExtraTurnsEstimate float64 `toml:"extra_turns_estimate" json:"extra_turns_estimate"`
}

// PerRequest is the fixed per-request token charge declared by the manifest.
func (o Overhead) PerRequest() int { return o.ToolDefTokens + o.InjectedTokens }

// Claim is the tool's own savings claim, recorded verbatim.
type Claim struct {
	Text      string  `toml:"text" json:"text"`
	Percent   float64 `toml:"percent" json:"percent,omitempty"`
	SourceURL string  `toml:"source_url" json:"source_url"`
}

// Manifest describes one external tool.
type Manifest struct {
	Name           string   `toml:"name" json:"name"`
	Version        string   `toml:"version" json:"version"` // pinned
	License        string   `toml:"license" json:"license"` // SPDX id or free text
	SourceURL      string   `toml:"source_url" json:"source_url"`
	VerifiedOn     string   `toml:"verified_on" json:"verified_on"` // YYYY-MM-DD
	ActsAs         ActsAs   `toml:"acts_as" json:"acts_as"`
	Category       Category `toml:"category" json:"category"`
	ConflictsWith  []string `toml:"conflicts_with" json:"conflicts_with,omitempty"`
	Points         []string `toml:"points" json:"points"`
	SupportsShadow bool     `toml:"supports_shadow" json:"supports_shadow"`
	ShadowNote     string   `toml:"shadow_note" json:"shadow_note,omitempty"`
	Install        Command  `toml:"install" json:"install"`
	Remove         Command  `toml:"remove" json:"remove"`
	Verify         Verify   `toml:"verify" json:"verify"`
	Wire           Wire     `toml:"wire" json:"wire"`
	// Cautions are facts about the tool the user should see before installing.
	Cautions []string `toml:"cautions" json:"cautions,omitempty"`
	// Rules is the text added to the agent's rules file (acts_as = rules_file only).
	Rules    string   `toml:"rules" json:"rules,omitempty"`
	Overhead Overhead `toml:"overhead" json:"overhead"`
	Claimed  Claim    `toml:"claimed" json:"claimed"`
}

// OptimizerName is the name used in config, measurement and reports.
func (m Manifest) OptimizerName() string { return "ext_" + m.Name }

// Validate rejects manifests Trove must not act on.
func (m Manifest) Validate() error {
	var bad []string
	need := func(ok bool, msg string) {
		if !ok {
			bad = append(bad, msg)
		}
	}
	need(validName(m.Name), "name must be lowercase letters, digits, - or _")
	need(m.Version != "" && !strings.ContainsAny(m.Version, "^~*<>= ") && m.Version != "latest", "version must be pinned exactly")
	need(m.License != "", "license is required")
	need(strings.HasPrefix(m.SourceURL, "https://"), "source_url must be https")
	need(len(m.VerifiedOn) == 10, "verified_on must be YYYY-MM-DD")
	switch m.ActsAs {
	case ActsShellWrapper, ActsMCPServer, ActsHook, ActsRulesFile:
	default:
		bad = append(bad, "acts_as must be shell_wrapper, mcp_server, hook or rules_file")
	}
	need(m.Category != "", "category is required")
	need(len(m.Points) > 0, "at least one engine point is required")
	for _, p := range m.Points {
		switch optimize.Point(p) {
		case optimize.PointToolInput, optimize.PointToolOutput, optimize.PointRequest:
		default:
			bad = append(bad, "unknown engine point "+p)
		}
	}
	if m.Install.Argv != nil {
		need(len(m.Install.Argv) > 0 && m.Install.Argv[0] != "", "install.argv must name a program")
	}
	if m.Remove.Argv != nil {
		need(len(m.Remove.Argv) > 0 && m.Remove.Argv[0] != "", "remove.argv must name a program")
	}
	if len(m.Verify.Argv) > 0 {
		need(m.Verify.Contains != "", "verify.contains is required when verify.argv is set")
	}
	switch m.ActsAs {
	case ActsHook:
		need(m.Wire.HookEvent != "" && m.Wire.HookCommand != "", "wire.hook_event and wire.hook_command are required for hook tools")
	case ActsMCPServer:
		need(len(m.Wire.Add) > 0 && len(m.Wire.Remove) > 0, "wire.add and wire.remove are required for MCP-server tools")
	}
	need((m.ActsAs == ActsRulesFile) == (strings.TrimSpace(m.Rules) != ""), "rules text is required for rules_file tools and not allowed for others")
	need(m.Overhead.ToolDefTokens >= 0 && m.Overhead.InjectedTokens >= 0 && m.Overhead.ExtraTurnsEstimate >= 0, "overhead must not be negative")
	need(m.Claimed.Text != "" && strings.HasPrefix(m.Claimed.SourceURL, "https://"), "claimed needs text and an https source_url")
	var out []string
	for _, b := range bad {
		if b != "" {
			out = append(out, b)
		}
	}
	if len(out) > 0 {
		return fmt.Errorf("manifest %q is invalid: %s", m.Name, strings.Join(out, "; "))
	}
	return nil
}

func validName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// Parse decodes and validates one manifest.
func Parse(data []byte) (Manifest, error) {
	var m Manifest
	dec := toml.NewDecoder(strings.NewReader(string(data))).DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("could not read manifest: %w", err)
	}
	return m, m.Validate()
}

// Builtin returns the manifests embedded in this binary, sorted by name.
func Builtin() ([]Manifest, error) {
	ents, err := manifestFS.ReadDir("manifests")
	if err != nil {
		return nil, err
	}
	var out []Manifest
	seen := map[string]bool{}
	for _, e := range ents {
		if !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		b, err := manifestFS.ReadFile("manifests/" + e.Name())
		if err != nil {
			return nil, err
		}
		m, err := Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		if seen[m.Name] {
			return nil, fmt.Errorf("duplicate manifest name %q", m.Name)
		}
		seen[m.Name] = true
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
