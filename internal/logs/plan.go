package logs

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Plan is how the user pays for the agent: per token with an API key, or with a
// subscription. It decides whether a history figure is shown as dollars spent
// or as API-equivalent value.
type Plan string

// Plan values.
const (
	PlanAPIKey       Plan = "api_key"
	PlanSubscription Plan = "subscription"
	PlanUnknown      Plan = "unknown"
)

// PlanResult is a detected plan and the signals behind it. Evidence holds the
// names of the signals only, never values.
type PlanResult struct {
	Plan     Plan     `json:"plan"`
	Evidence []string `json:"evidence"`
	// Estimated is always true: the plan is inferred from local signals, not
	// confirmed with the provider.
	Estimated bool `json:"estimated"`
}

// DetectClaudeCodePlan infers the plan from signals that are not credentials:
//
//   - whether ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN is set (presence only; the
//     value is never read into a variable that outlives the check),
//   - the apiKeyHelper and forceLoginMethod keys of settings.json,
//   - ANTHROPIC_BASE_URL pointing somewhere other than a subscription login.
//
// It never opens ~/.claude.json, credential files or the keychain. When the
// signals conflict or are absent the answer is PlanUnknown. override, if set
// to a valid plan (config [plan]), wins.
func DetectClaudeCodePlan(getenv func(string) string, configDir, override string) PlanResult {
	if override != "" {
		switch p := Plan(override); p {
		case PlanAPIKey, PlanSubscription:
			return PlanResult{Plan: p, Evidence: []string{"config:plan"}, Estimated: true}
		}
	}
	var apiSignals, subSignals []string
	if getenv("ANTHROPIC_API_KEY") != "" {
		apiSignals = append(apiSignals, "env:ANTHROPIC_API_KEY")
	}
	if getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		apiSignals = append(apiSignals, "env:ANTHROPIC_AUTH_TOKEN")
	}
	if configDir != "" {
		b, err := os.ReadFile(filepath.Join(configDir, "settings.json")) //nolint:gosec // the agent's own settings file, read for two key names
		if err == nil {
			var s struct {
				APIKeyHelper     string `json:"apiKeyHelper"`
				ForceLoginMethod string `json:"forceLoginMethod"`
			}
			if json.Unmarshal(b, &s) == nil {
				if s.APIKeyHelper != "" {
					apiSignals = append(apiSignals, "settings:apiKeyHelper")
				}
				switch s.ForceLoginMethod {
				case "claudeai":
					subSignals = append(subSignals, "settings:forceLoginMethod=claudeai")
				case "console":
					apiSignals = append(apiSignals, "settings:forceLoginMethod=console")
				}
			}
		}
	}
	switch {
	case len(apiSignals) > 0 && len(subSignals) == 0:
		return PlanResult{Plan: PlanAPIKey, Evidence: apiSignals, Estimated: true}
	case len(subSignals) > 0 && len(apiSignals) == 0:
		return PlanResult{Plan: PlanSubscription, Evidence: subSignals, Estimated: true}
	default:
		return PlanResult{Plan: PlanUnknown, Evidence: append(apiSignals, subSignals...), Estimated: true}
	}
}

// DetectCodexPlan infers the plan for Codex CLI. The only non-credential signal
// is whether OPENAI_API_KEY is set (presence only). A ChatGPT login is stored in
// auth.json or the keychain, which Trove never opens, so without the key or a
// config override the answer is PlanUnknown.
func DetectCodexPlan(getenv func(string) string, override string) PlanResult {
	switch Plan(override) {
	case PlanAPIKey, PlanSubscription:
		return PlanResult{Plan: Plan(override), Evidence: []string{"config:plan"}, Estimated: true}
	}
	if getenv("OPENAI_API_KEY") != "" {
		return PlanResult{Plan: PlanAPIKey, Evidence: []string{"env:OPENAI_API_KEY"}, Estimated: true}
	}
	return PlanResult{Plan: PlanUnknown, Estimated: true}
}
