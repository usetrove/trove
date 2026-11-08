// Package journey simulates realistic users of Trove over a simulated week and
// measures what the request optimizers change in tokens and list-price cost.
//
// The offline tier (this package) is deterministic and spends nothing. It feeds
// synthetic but seeded agent sessions through the real optimizer engine
// (internal/optimize) and prices both arms with the pinned pricing table
// (internal/pricing). Every number it produces is ESTIMATED. The live tier
// (bench/, Python) is the only place a bill is measured.
package journey

// Kind is the type of one tool output in a simulated session.
type Kind string

const (
	KindReadSmall   Kind = "read_small"
	KindReadLarge   Kind = "read_large"
	KindGrep        Kind = "grep"
	KindTestVerbose Kind = "test_verbose"
	KindStackTrace  Kind = "stack_trace"
	KindBashSmall   Kind = "bash_small"
	KindBuildLog    Kind = "build_log"
)

// Kinds lists every output kind in a fixed order. Sampling walks this order so
// results do not depend on map iteration.
var Kinds = []Kind{KindReadSmall, KindReadLarge, KindGrep, KindTestVerbose, KindStackTrace, KindBashSmall, KindBuildLog}

// Adapter says how a profile reaches Trove. It decides which arm the profile can
// be measured in.
type Adapter string

const (
	// AdapterGateway: the agent's requests go through the local gateway (API-key users).
	AdapterGateway Adapter = "gateway"
	// AdapterHooks: the Claude Code plugin hooks. Request-level optimizers are
	// not reachable this way, so the offline request model does not apply; see
	// Profile.Measurable.
	AdapterHooks Adapter = "hooks"
	// AdapterSettings: settings autopilot only (Codex profiles). No request-level
	// transform applies, and profile-scoped sessions cannot be attributed.
	AdapterSettings Adapter = "settings"
)

// Profile describes one kind of user. All counts are per simulated week.
type Profile struct {
	ID          string
	Name        string
	Description string
	Adapter     Adapter
	// Billing is "api-key" (dollars can be measured live) or "subscription"
	// (dollars cannot; tokens only).
	Billing string
	// Sessions is the number of agent sessions in one simulated week.
	Sessions int
	// TurnsMin and TurnsMax bound the tool calls per session (inclusive).
	TurnsMin, TurnsMax int
	// Mix weights each output kind. Missing kinds have weight zero.
	Mix map[Kind]int
	// RepeatRead is the chance (0 to 100) that a turn re-reads output the
	// session already saw.
	RepeatRead int
	// ToolsDescBytes is the size of the agent's own tool definitions in a
	// request. Subagent-heavy profiles send more.
	ToolsDescBytes int
	// Subagents is the chance (0 to 100) that a session also spawns a subagent
	// whose tool outputs are added to the parent's history.
	Subagents int
}

// Measurable reports whether the offline request model applies to the profile.
// Profiles that return false are reported as not measurable, with the reason.
func (p Profile) Measurable() (bool, string) {
	switch p.Model() {
	case ModelGateway, ModelHooks:
		return true, ""
	}
	return false, p.NotMeasurableReason()
}

// Model names the simulation a profile gets.
type Model string

const (
	// ModelGateway: request-level. Tokens and list-price dollars (API-key users).
	ModelGateway Model = "gateway"
	// ModelHooks: tool-level, through the real hook optimizers. Tokens only, because
	// subscription traffic is never priced in dollars here and is never routed
	// through the gateway.
	ModelHooks Model = "hooks"
	// ModelNone: not measurable here.
	ModelNone Model = "none"
)

// Model returns the simulation for the profile.
func (p Profile) Model() Model {
	switch {
	case p.Adapter == AdapterGateway && p.Billing == "api-key":
		return ModelGateway
	case p.Adapter == AdapterHooks || p.Billing == "subscription":
		return ModelHooks
	}
	return ModelNone
}

// NotMeasurableReason says why a profile has no simulation.
func (p Profile) NotMeasurableReason() string {
	if p.Model() != ModelNone {
		return ""
	}
	if p.Adapter == AdapterSettings {
		return "settings autopilot only; no request-level or tool-level transform applies, and profile-scoped sessions cannot be attributed (see docs/settings.md)"
	}
	return "no simulation is defined for this adapter"
}

// Profiles returns the built-in user profiles in a fixed order.
func Profiles() []Profile {
	return []Profile{
		{
			ID: "light", Name: "Light user", Description: "A few short tasks a day in a small repo.",
			Adapter: AdapterGateway, Billing: "api-key", Sessions: 6, TurnsMin: 4, TurnsMax: 10,
			Mix:        map[Kind]int{KindReadSmall: 40, KindBashSmall: 30, KindGrep: 15, KindTestVerbose: 15},
			RepeatRead: 10, ToolsDescBytes: 14000,
		},
		{
			ID: "daily-pro", Name: "Daily pro", Description: "A full workday of mixed tasks on a mid-size repo.",
			Adapter: AdapterGateway, Billing: "api-key", Sessions: 14, TurnsMin: 15, TurnsMax: 40,
			Mix:        map[Kind]int{KindReadSmall: 30, KindReadLarge: 10, KindGrep: 20, KindTestVerbose: 15, KindBashSmall: 15, KindStackTrace: 5, KindBuildLog: 5},
			RepeatRead: 20, ToolsDescBytes: 14000,
		},
		{
			ID: "heavy-agentic", Name: "Heavy agentic", Description: "Long sessions, many tool calls, subagents.",
			Adapter: AdapterGateway, Billing: "api-key", Sessions: 8, TurnsMin: 60, TurnsMax: 150,
			Mix:        map[Kind]int{KindReadSmall: 30, KindReadLarge: 15, KindGrep: 20, KindTestVerbose: 15, KindBashSmall: 10, KindBuildLog: 10},
			RepeatRead: 25, ToolsDescBytes: 22000, Subagents: 50,
		},
		{
			ID: "monorepo-explorer", Name: "Monorepo explorer", Description: "Lots of search, file reads and navigation in a large repo.",
			Adapter: AdapterGateway, Billing: "api-key", Sessions: 10, TurnsMin: 30, TurnsMax: 80,
			Mix:        map[Kind]int{KindGrep: 40, KindReadLarge: 25, KindReadSmall: 25, KindBashSmall: 10},
			RepeatRead: 30, ToolsDescBytes: 14000,
		},
		{
			ID: "tdd", Name: "Test-heavy / TDD", Description: "Frequent test runs with verbose output.",
			Adapter: AdapterGateway, Billing: "api-key", Sessions: 12, TurnsMin: 25, TurnsMax: 70,
			Mix:        map[Kind]int{KindTestVerbose: 40, KindReadSmall: 25, KindBashSmall: 15, KindStackTrace: 10, KindGrep: 10},
			RepeatRead: 15, ToolsDescBytes: 14000,
		},
		{
			ID: "debugger", Name: "Debugger", Description: "Logs, stack traces and retries.",
			Adapter: AdapterGateway, Billing: "api-key", Sessions: 9, TurnsMin: 20, TurnsMax: 60,
			Mix:        map[Kind]int{KindStackTrace: 25, KindBuildLog: 20, KindTestVerbose: 20, KindGrep: 15, KindReadSmall: 15, KindBashSmall: 5},
			RepeatRead: 35, ToolsDescBytes: 14000,
		},
		{
			ID: "indie-gateway", Name: "API-key indie hacker", Description: "Opts into the gateway; pays per token.",
			Adapter: AdapterGateway, Billing: "api-key", Sessions: 10, TurnsMin: 15, TurnsMax: 45,
			Mix:        map[Kind]int{KindReadSmall: 35, KindGrep: 20, KindTestVerbose: 20, KindBashSmall: 15, KindBuildLog: 10},
			RepeatRead: 15, ToolsDescBytes: 14000,
		},
		{
			ID: "subscription-pro", Name: "Subscription pro", Description: "Claude Pro/Max subscription user. Trove never handles subscription traffic, so this profile reaches it through hooks only and is not measured in dollars.",
			Adapter: AdapterHooks, Billing: "subscription", Sessions: 14, TurnsMin: 15, TurnsMax: 40,
			Mix:        map[Kind]int{KindReadSmall: 30, KindReadLarge: 10, KindGrep: 20, KindTestVerbose: 15, KindBashSmall: 15, KindStackTrace: 5, KindBuildLog: 5},
			RepeatRead: 20, ToolsDescBytes: 14000,
		},
		{
			ID: "codex", Name: "Codex user", Description: "Settings autopilot and Codex-side session features only.",
			Adapter: AdapterSettings, Billing: "api-key", Sessions: 10, TurnsMin: 15, TurnsMax: 40,
			Mix:        map[Kind]int{KindReadSmall: 30, KindGrep: 20, KindTestVerbose: 20, KindBashSmall: 30},
			RepeatRead: 15, ToolsDescBytes: 9000,
		},
	}
}
