// Package config loads and validates ~/.trove/config.toml.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the config file inside the data directory.
const FileName = "config.toml"

// Optimizer states.
const (
	StateOff    = "off"
	StateShadow = "shadow"
	StateOn     = "on"
)

// Config is the full Trove configuration.
type Config struct {
	Gateway       Gateway           `toml:"gateway"`
	Upstreams     Upstreams         `toml:"upstreams"`
	Optimizers    map[string]string `toml:"optimizers"`
	Optimize      Optimize          `toml:"optimize"`
	Budget        Budget            `toml:"budget"`
	Measure       Measure           `toml:"measure"`
	Experiment    Experiment        `toml:"experiment"`
	Notifications Notifications     `toml:"notifications"`
	History       History           `toml:"history"`
	Fixer         Fixer             `toml:"fixer"`
	Settings      Settings          `toml:"settings"`
	Tasks         Tasks             `toml:"tasks"`
	Recovery      Recovery          `toml:"recovery"`
	Contribute    Contribute        `toml:"contribute"`
	RetentionDays int               `toml:"retention_days"`

	// autopilot holds the states Autopilot chose for optimizers the user did not set
	// themselves (see autopilot.go). It is not part of config.toml.
	autopilot map[string]string
	// trials names the optimizers Autopilot has on trial (see AutopilotProbation), for readers that assign
	// each session an arm themselves.
	trials []string
}

// Contribute holds the setting for the optional anonymous summary upload (see
// PRIVACY.md). Whether upload is switched on is not here: it is a consent record
// kept in contribute.json by `trove contribute enable`.
type Contribute struct {
	// Endpoint is the HTTPS URL that receives the summary. Empty means upload is
	// not possible, whatever the consent record says. Having a default here sends
	// nothing: upload also needs the consent record that only
	// `trove contribute enable` writes.
	Endpoint string `toml:"endpoint"`
}

// DefaultContributeEndpoint is where the optional anonymous summary goes. The
// receiver's code is in server/worker.
const DefaultContributeEndpoint = "https://contribute.troverelay.com/v1/contribute"

// Fixer holds the settings for `trove fix` (see docs/fixer.md).
type Fixer struct {
	// MinSessions is how many recent sessions in a project Trove must have seen
	// before it calls an MCP server unused there.
	MinSessions int `toml:"min_sessions"`
	// LookbackDays is how far back that history reaches.
	LookbackDays int `toml:"lookback_days"`
	// ContextMinTokens is the size (estimated tokens) above which a context file
	// is checked for removable sections.
	ContextMinTokens int `toml:"context_min_tokens"`
	// ActivityMinutes: a fix is not applied while an agent session has been
	// active this recently; it is queued instead.
	ActivityMinutes int `toml:"activity_minutes"`
	// RetypedMinCount and RetypedMinSessions: a Bash command the agent typed at
	// least this many times, in at least this many sessions, is offered as a
	// trove_run command (docs/fixer.md).
	RetypedMinCount    int `toml:"retyped_min_count"`
	RetypedMinSessions int `toml:"retyped_min_sessions"`
}

// Settings holds the settings for `trove settings` (see docs/settings.md).
type Settings struct {
	// DefaultTask is the task type `trove settings plan` uses when --task is
	// not given: quick-fix, feature, refactor or exploration. Empty means the
	// command asks for one.
	DefaultTask string `toml:"default_task"`
	// AllowUnproven lets profiles include values that lower a setting below the
	// harness default even though no benchmark or A/B evidence supports them.
	// Off by default: shipped profiles never lower anything on their own.
	AllowUnproven bool `toml:"allow_unproven"`
}

// History holds the settings for reading agents' local session logs
// (see docs/logs.md). Trove only reads them; it never changes them.
type History struct {
	// Enabled lets `trove` read local session logs (Claude Code, Codex CLI).
	Enabled bool `toml:"enabled"`
	// Plan overrides plan detection: "api_key" or "subscription". Empty detects it
	// from settings and environment (estimated).
	Plan string `toml:"plan"`
}

// Notifications holds the optional desktop notification settings.
type Notifications struct {
	// Desktop shows a desktop notification with the receipt when a session ends.
	// Off by default.
	Desktop bool `toml:"desktop"`
}

// Measure holds the settings for baselines, interleaving and guardrails
// (see docs/measurement.md).
type Measure struct {
	// BaselineDays and BaselineSessions: until BOTH have passed since install,
	// every optimizer runs in shadow mode only.
	BaselineDays     int `toml:"baseline_days"`
	BaselineSessions int `toml:"baseline_sessions"`
	// Interleave alternates whole sessions between on and off for a cleaner comparison.
	Interleave bool `toml:"interleave"`
	// GitSignals counts commits made during or shortly after a session (read-only).
	GitSignals bool `toml:"git_signals"`
	// GitRepo is the local repository to count commits in. The gateway cannot
	// know which repo an agent works in (Trove never stores file paths), so the
	// user names it here.
	GitRepo string `toml:"git_repo"`
	// MinSessions is the smallest sample, per side, before any guardrail may fire
	// or any comparison is reported as a conclusion.
	MinSessions int `toml:"min_sessions"`
	// RetryIncreasePercent triggers rollback when repeated identical tool calls
	// per session are this much higher than the comparison.
	RetryIncreasePercent int `toml:"retry_increase_percent"`
	// RefetchRatePercent triggers rollback when this percent of sessions that an
	// optimizer acted on re-fetch what it removed.
	RefetchRatePercent int `toml:"refetch_rate_percent"`
}

// Experiment holds the settings for session-level A/B comparison of optimizers.
type Experiment struct {
	// Enabled alternates whole sessions between an optimizer on and in shadow
	// (only for optimizers the user set to "on"). Off means shadow estimates only.
	Enabled bool `toml:"enabled"`
	// Seed makes assignment reproducible. Any number.
	Seed int64 `toml:"seed"`
	// MinSessionsPerArm is the fewest sessions in each arm before a measured
	// claim is made. Below it Trove says "not enough data yet".
	MinSessionsPerArm int `toml:"min_sessions_per_arm"`
	// CostIncreasePercent rolls an optimizer back when the measured cost per
	// session is this much higher with it on (and the whole range is above zero).
	CostIncreasePercent int `toml:"cost_increase_percent"`
	// OutcomeWorsenPercent rolls an optimizer back when turns, tool errors or
	// abandoned sessions rise by this much (and the whole range is above zero).
	OutcomeWorsenPercent int `toml:"outcome_worsen_percent"`
}

// Gateway holds local gateway settings. It always binds to 127.0.0.1.
type Gateway struct {
	Port        int `toml:"port"`
	IdleMinutes int `toml:"idle_minutes"`
	// KeepWarmMaxPings is how many keep-warm pings one idle session may get (1 to 6, default 2).
	// KeepWarmDailyCapUSD caps what pings may spend in a local day (above 0, at most $1, the
	// default). Both may only be lowered: see docs/decisions/keep-warm.md.
	KeepWarmMaxPings    int     `toml:"keep_warm_max_pings"`
	KeepWarmDailyCapUSD float64 `toml:"keep_warm_daily_cap_usd"`
}

// Optimize holds settings shared by all optimizers. Per-optimizer states live
// in Optimizers.
type Optimize struct {
	// CustomAgents lists the agent labels (see gateway/agent) the user has
	// explicitly marked as custom agents that Trove may give a cache layout.
	CustomAgents []string `toml:"custom_agents"`
	// BudgetMS is the time the whole optimizer pipeline may take per request
	// before Trove forwards the original request unmodified.
	BudgetMS int `toml:"budget_ms"`
	// CacheTTL sets the lifetime of each cache marker that cache_breakpoints adds
	// for custom agents.
	CacheTTL CacheTTL `toml:"cache_ttl"`
	// Verbosity is the note verbosity_steer appends to the system prompt:
	// VerbosityConcise (default) or VerbosityTerse. Pinned per session.
	Verbosity string `toml:"verbosity"`
	// RouteTo is the cheaper model model_router may step down to on tool turns
	// (plan item 7.2). Empty means model_router does nothing. Pinned per session.
	RouteTo string `toml:"route_to"`
	// ColdResumeAction is what hook_cold_resume does when it is on: ColdResumeWarn (default) shows a warning
	// and lets the prompt go through, ColdResumeAsk stops the first prompt after a cold resume once. Pinned per
	// session.
	ColdResumeAction string `toml:"cold_resume_action"`
	// ColdResumeMinTokens and ColdResumeMinWeekPct are the rebuild sizes from which hook_cold_resume acts: the
	// tokens the next request would write to the cache again, or the low end of its estimated share of the
	// weekly limit. Either one is enough. Pinned per session.
	ColdResumeMinTokens  int64   `toml:"cold_resume_min_tokens"`
	ColdResumeMinWeekPct float64 `toml:"cold_resume_min_week_pct"`
}

// Actions for hook_cold_resume.
const (
	ColdResumeWarn = "warn"
	ColdResumeAsk  = "ask"
)

// Verbosity levels for verbosity_steer.
const (
	VerbosityConcise = "concise"
	VerbosityTerse   = "terse"
)

// CacheTTL is the lifetime of each cache marker cache_breakpoints adds, in
// prompt order: last tool definition, last stable system block, then the
// top-level marker that covers the messages. Each is "5m" (or empty, the
// provider's default) or "1h". The provider requires a longer lifetime to come
// before a shorter one (verified 2026-11-17 against
// https://platform.claude.com/docs/en/build-with-claude/prompt-caching), so a
// later marker may never outlive an earlier one.
type CacheTTL struct {
	Tools    string `toml:"tools" json:"tools,omitempty"`
	System   string `toml:"system" json:"system,omitempty"`
	Messages string `toml:"messages" json:"messages,omitempty"`
}

// ttlRank orders lifetimes; 0 means the value is not allowed.
func ttlRank(v string) int {
	switch v {
	case "", "5m":
		return 1
	case "1h":
		return 2
	}
	return 0
}

// Check reports an invalid value or an ordering where a later marker would
// outlive an earlier one. The error names the config key to change.
func (c CacheTTL) Check() error {
	fields := []struct{ name, v string }{{"tools", c.Tools}, {"system", c.System}, {"messages", c.Messages}}
	for _, f := range fields {
		if ttlRank(f.v) == 0 {
			return fmt.Errorf("optimize.cache_ttl.%s is %q; use \"5m\" or \"1h\"", f.name, f.v)
		}
	}
	for i := 1; i < len(fields); i++ {
		if ttlRank(fields[i].v) > ttlRank(fields[i-1].v) {
			return fmt.Errorf("optimize.cache_ttl.%s is 1h but optimize.cache_ttl.%s is 5m; a longer lifetime must come before a shorter one (tools, then system, then messages), so set %s to \"1h\" or %s to \"5m\"",
				fields[i].name, fields[i-1].name, fields[i-1].name, fields[i].name)
		}
	}
	return nil
}

// IsDefault reports whether every marker uses the 5-minute default.
func (c CacheTTL) IsDefault() bool {
	return ttlRank(c.Tools) == 1 && ttlRank(c.System) == 1 && ttlRank(c.Messages) == 1
}

// Optimizers with extra requirements before "on" takes effect.
const (
	// OptimizerOutputTrimmer needs the Trove MCP server registered with the agent
	// (so it can call trove_fetch_full) and a working recovery store. Until then
	// "on" runs as "shadow": the engine clamps it when it pins a session.
	OptimizerOutputTrimmer = "output_trimmer"
	// OptimizerHookRepeatRead denies a Read of a file the agent already read in
	// this session and that has not changed (Claude Code hooks, see docs/hooks.md).
	OptimizerHookRepeatRead = "hook_repeat_read"
	// OptimizerHookLargeOutput adds a hint to run a known-noisy command through
	// trove_run. It never blocks.
	OptimizerHookLargeOutput = "hook_large_output"
	// OptimizerHookReadLimit adds a line limit to a Read of a large file that has
	// none. The agent can still ask for more with offset and limit.
	OptimizerHookReadLimit = "hook_read_limit"
	// OptimizerHookQuietCommands adds output-quieting flags to allowlisted shell
	// commands. It never changes what a command does.
	OptimizerHookQuietCommands = "hook_quiet_commands"
	// OptimizerHookOutputDedup replaces a Bash, Grep or Glob output that is identical to one
	// earlier in the session with a one-line pointer. Like the output trimmer it needs the
	// Trove MCP server and the recovery store before "on" takes effect.
	OptimizerHookOutputDedup = "hook_output_dedup"
	// OptimizerHookReadOutline replaces a very large full Read of a source file with its outline. Like the
	// output trimmer it needs the Trove MCP server and the recovery store before "on" takes effect.
	OptimizerHookReadOutline = "hook_read_outline"
	// OptimizerHookColdResume warns, or asks once, before a prompt that would write a large session's whole
	// context to the cache again because the cache expired (UserPromptSubmit, docs/hooks.md). It changes no
	// request; optimize.cold_resume_action says which.
	OptimizerHookColdResume = "hook_cold_resume"
	// OptimizerHookModelSwitch adds the cost of rebuilding a large, warm context on another model to Claude Code's
	// own switch confirmation (PreModelSwitch systemMessage). It never blocks or asks.
	OptimizerHookModelSwitch = "hook_model_switch"
	// OptimizerHookColdCompact stops a manual /compact once when the cache has expired and the history is large,
	// because the summarization would reprocess it as uncached input (PreCompact, exit 2).
	OptimizerHookColdCompact = "hook_cold_compact"
	// OptimizerNoiseNormalizer is lossless; it also runs in hooks on tool output.
	OptimizerNoiseNormalizer = "noise_normalizer"
	// OptimizerInflightDedup is a behavior of the gateway itself: when identical requests
	// arrive while one is still waiting for the provider, they could share that one call.
	// Only its shadow mode exists: it estimates the saving and changes nothing.
	OptimizerInflightDedup = "inflight_dedup"
	// OptimizerSpendCap is a behavior of the gateway itself: when spending in the budget period
	// has reached the budget cap, a hard cap would refuse further requests. Only its shadow mode
	// exists: it estimates what the cap would have stopped and refuses nothing.
	OptimizerSpendCap = "spend_cap"
	// OptimizerKeepWarm is a behavior of the gateway itself: while an API-key session is idle it
	// re-reads the session's cached prefix so the 5-minute cache does not expire. Shadow estimates
	// only; on sends pings. Approved, with its limits, in docs/decisions/keep-warm.md.
	OptimizerKeepWarm = "keep_warm"
)

// EnvOptimize is the kill-switch environment variable. Set to "off", it
// disables every transformation immediately.
const EnvOptimize = "TROVE_OPTIMIZE"

// Upstreams are the provider origins the gateway forwards to.
type Upstreams struct {
	Anthropic string `toml:"anthropic"`
	OpenAI    string `toml:"openai"`
	// Cloud backends and OpenAI-compatible servers are off until configured.
	Vertex  string           `toml:"vertex"`  // origin, e.g. https://aiplatform.googleapis.com
	Foundry string           `toml:"foundry"` // base URL incl. /anthropic
	Bedrock string           `toml:"bedrock"` // origin, e.g. https://bedrock-runtime.us-east-1.amazonaws.com
	Compat  []CompatUpstream `toml:"compat"`
}

// Budget holds spending-cap settings. A CapUSD of 0 means no cap.
type Budget struct {
	Period        string  `toml:"period"`
	CapUSD        float64 `toml:"cap_usd"`
	WarnAtPercent int     `toml:"warn_at_percent"`
	// UsageUSD is a budget for usage that is not billed per token (a subscription),
	// in API-equivalent dollars per period. The status line warns against it and
	// nothing is ever stopped. 0 means none.
	UsageUSD float64 `toml:"usage_usd"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Gateway:    Gateway{Port: 8787, IdleMinutes: 30, KeepWarmMaxPings: 2, KeepWarmDailyCapUSD: 1},
		Upstreams:  Upstreams{Anthropic: "https://api.anthropic.com", OpenAI: "https://api.openai.com"},
		Optimizers: map[string]string{},
		Optimize: Optimize{CustomAgents: []string{}, BudgetMS: 25, Verbosity: VerbosityConcise,
			ColdResumeAction: ColdResumeWarn, ColdResumeMinTokens: 150000, ColdResumeMinWeekPct: 3},
		Budget: Budget{Period: "monthly", CapUSD: 0, WarnAtPercent: 80},
		Measure: Measure{
			BaselineDays: 7, BaselineSessions: 20, MinSessions: 20,
			RetryIncreasePercent: 30, RefetchRatePercent: 25,
		},
		Experiment: Experiment{
			Enabled: true, Seed: 20261104, MinSessionsPerArm: 20,
			CostIncreasePercent: 5, OutcomeWorsenPercent: 20,
		},
		RetentionDays: 90,
		History:       History{Enabled: true},
		Fixer:         Fixer{MinSessions: 5, LookbackDays: 30, ContextMinTokens: 1500, ActivityMinutes: 30, RetypedMinCount: 5, RetypedMinSessions: 3},
		Recovery:      Recovery{MaxMB: 200, TTLDays: 7},
		Contribute:    Contribute{Endpoint: DefaultContributeEndpoint},
	}
}

// OptimizerState returns the state of the named optimizer; unlisted means off.
func (c Config) OptimizerState(name string) string {
	if s, ok := c.Optimizers[name]; ok {
		return s
	}
	if s, ok := c.autopilot[name]; ok {
		return s
	}
	return StateOff
}

// DefaultTOML is the documented default config file written by `trove setup`.
const DefaultTOML = `# Trove configuration. Every value shown is the default.
# Environment variables prefixed TROVE_ override this file
# (for example TROVE_GATEWAY_PORT=9000).

# Delete usage rows older than this many days (1 to 3650).
retention_days = 90

[gateway]
# Local port for the opt-in gateway. It always listens on 127.0.0.1 only.
port = 8787
# A gap longer than this many minutes between requests starts a new session (1 to 1440).
idle_minutes = 30
# Keep-warm pings (the keep_warm optimizer, API-key gateway traffic only): at most this many
# pings per idle session (1 to 6) and this much ping spend per local day (above 0, at most 1 dollar).
keep_warm_max_pings = 2
keep_warm_daily_cap_usd = 1.0

[upstreams]
# Where the gateway forwards your own API requests. Origin only, no path.
anthropic = "https://api.anthropic.com"
openai = "https://api.openai.com"

[optimizers]
# Each optimizer is "off", "shadow" (estimate only, change nothing) or "on".
# Optimizers not listed here are off. Changes take effect at the next session.
# example = "shadow"

[optimize]
# Agent labels you explicitly mark as custom agents, for optimizers that give a
# custom agent a better cache layout (cache_breakpoints, volatile_prefix_mover).
# Allowed labels: "sdk", "other". Claude Code and Codex are never allowed.
custom_agents = []
# If the optimizers take longer than this many milliseconds (1 to 1000), Trove
# forwards your original request unmodified.
budget_ms = 25
# How short verbosity_steer asks replies to be: "concise" or "terse". It only
# matters while verbosity_steer is on, and changes apply to new sessions.
verbosity = "concise"
# The cheaper model model_router may step down to on tool turns, for API-key
# traffic only. Empty means model_router does nothing. Changes apply to new sessions.
route_to = ""
# What hook_cold_resume does when it is on: "warn" shows what continuing a session
# whose cache expired will cost and lets the prompt go through; "ask" stops the first
# prompt after resuming such a session, once, so you can /clear instead. Changes
# apply to new sessions.
cold_resume_action = "warn"
# hook_cold_resume acts from this many tokens re-written to the cache (10000 to
# 2000000), or from this estimated share of the weekly limit, in percent (above 0, at
# most 100), whichever is reached first.
cold_resume_min_tokens = 150000
cold_resume_min_week_pct = 3.0

[budget]
# Budget period: "daily", "weekly" or "monthly".
period = "monthly"
# Spending cap in USD for the period. 0 means no cap.
cap_usd = 0.0
# Warn when this percent of the cap is used (1 to 100).
warn_at_percent = 80
# Subscription usage budget in API-equivalent USD for the period. The status line
# shows your usage against it and warns; nothing is ever stopped. 0 means none.
usage_usd = 0.0

[measure]
# For the first baseline_days days AND baseline_sessions sessions after install,
# every optimizer runs in shadow mode only (estimates, nothing changed).
baseline_days = 7
baseline_sessions = 20
# Alternate whole sessions between on and off for a cleaner comparison. Off by
# default: the off sessions forgo the savings. Sessions are never switched mid-way.
interleave = false
# Count commits made during or shortly after a session, read-only, from the local
# git repo. Only counts are stored, never messages, file names or diffs.
git_signals = false
# The repository to count commits in when git_signals is on (a folder path).
git_repo = ""
# Fewest sessions on each side before a guardrail may fire or a comparison is
# called a conclusion. Below this Trove says "not enough data yet".
min_sessions = 20
# Roll an optimizer back to shadow when repeated identical tool calls per session
# rise by this percent (1 to 1000), or when this percent of sessions it acted on
# re-fetch what it removed (1 to 100).
retry_increase_percent = 30
refetch_rate_percent = 25

[experiment]
# Whole sessions alternate between an optimizer you set to "on" and shadow mode,
# so Trove can compare your real bill and outcomes. Sessions are never switched
# mid-way. Set to false to opt out: you then get shadow estimates only.
enabled = true
# Makes the alternation reproducible. Any number.
seed = 20261104
# Fewest sessions in each arm before a measured result is shown (5 to 10000).
min_sessions_per_arm = 20
# Roll an optimizer back to shadow when measured cost per session is this percent
# higher with it on (1 to 1000), or when turns, tool errors or abandoned sessions
# are this percent higher (1 to 1000), and the whole confidence range agrees.
cost_increase_percent = 5
outcome_worsen_percent = 20

[notifications]
# Show a desktop notification with the receipt when a session ends.
desktop = false

[history]
# Read your agent's own session logs (Claude Code, Codex CLI) so trove can
# report without the gateway. Read-only: Trove never edits them and stores only
# counts, sizes and hashes, never prompts, code, paths or tool output.
enabled = true
# How you pay for the agent: "api_key" or "subscription". Leave empty to let Trove
# estimate it from your settings and environment (it never reads credentials).
plan = ""

[fixer]
# "trove fix" proposes edits to your agent's config files. Nothing changes until
# you approve a fix, every changed file is backed up, and "trove undo" reverses it.
# How many recent sessions in a project Trove must have seen before it calls an
# MCP server unused there (3 to 1000), and how many days back to look (1 to 365).
min_sessions = 5
lookback_days = 30
# Context files (CLAUDE.md, AGENTS.md) larger than this many estimated tokens are
# checked for sections that can be removed (100 to 100000).
context_min_tokens = 1500
# Fixes are not applied while an agent session was active this recently (minutes,
# 1 to 1440); they are queued and applied when the agent is idle.
activity_minutes = 30
# A shell command the agent retyped at least retyped_min_count times (2 to 1000)
# in at least retyped_min_sessions sessions (1 to 1000) is offered as a trove_run
# command in .trove/commands.toml.
retyped_min_count = 5
retyped_min_sessions = 3

[settings]
# "trove settings" proposes per-task-type values for your agent's built-in token
# controls (thinking, compaction, MCP limits). It goes through the same approval,
# backup and undo as "trove fix", and never overrides managed settings.
# Task type used when --task is not given: quick-fix, feature, refactor or
# exploration. Empty asks each time.
default_task = ""
# Profiles never lower a setting below the harness default unless evidence says
# it is safe. Set true to allow lowering values that are not yet measured.
allow_unproven = false

[recovery]
# The MCP tools keep the full text of any output they shorten here, in
# ~/.trove/recovery (owner-only files), so nothing is ever lost.
# Total size limit in MB (1 to 10000); the oldest outputs are removed first.
max_mb = 200
# Days an output is kept (1 to 365).
ttl_days = 7

[contribute]
# Where "trove contribute enable" sends the anonymous summary you approved.
# Nothing is sent unless you run "trove contribute enable", whatever this says.
# Leave this line out to use Trove's built-in address, so a newer Trove can change
# it for you. Uncomment it only to send somewhere else, or set it to "" to make
# sending impossible.
# endpoint = "https://example.com/v1/contribute"
`

var optimizerName = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Validate checks c and returns every problem found, in plain English.
func (c Config) Validate() error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Gateway.Port < 1 || c.Gateway.Port > 65535 {
		add("gateway.port is %d; use a port from 1 to 65535 (default 8787).", c.Gateway.Port)
	}
	if c.Gateway.IdleMinutes < 1 || c.Gateway.IdleMinutes > 1440 {
		add("gateway.idle_minutes is %d; use a number of minutes from 1 to 1440 (default 30).", c.Gateway.IdleMinutes)
	}
	if c.Gateway.KeepWarmMaxPings < 1 || c.Gateway.KeepWarmMaxPings > 6 {
		add("gateway.keep_warm_max_pings is %d; use a number from 1 to 6 (default 2).", c.Gateway.KeepWarmMaxPings)
	}
	if !(c.Gateway.KeepWarmDailyCapUSD > 0 && c.Gateway.KeepWarmDailyCapUSD <= 1) {
		add("gateway.keep_warm_daily_cap_usd is %v; use an amount above 0 and at most 1 dollar (default 1).", c.Gateway.KeepWarmDailyCapUSD)
	}
	for name, v := range map[string]string{"upstreams.anthropic": c.Upstreams.Anthropic, "upstreams.openai": c.Upstreams.OpenAI} {
		if problem := checkUpstream(v); problem != "" {
			add("%s %s", name, problem)
		}
	}
	for name, v := range map[string]string{"upstreams.vertex": c.Upstreams.Vertex, "upstreams.bedrock": c.Upstreams.Bedrock} {
		if v == "" {
			continue
		}
		if problem := checkUpstream(v); problem != "" {
			add("%s %s", name, problem)
		}
	}
	if v := c.Upstreams.Foundry; v != "" {
		if problem := ValidateUpstreamBase(v); problem != "" {
			add("upstreams.foundry %s", problem)
		}
	}
	seenCompat := map[string]bool{}
	for _, cu := range c.Upstreams.Compat {
		if problem := ValidateCompat(cu); problem != "" {
			add("upstreams.compat %q %s", cu.Name, problem)
		}
		if seenCompat[cu.Name] {
			add("upstreams.compat has two entries named %q; names must be unique.", cu.Name)
		}
		seenCompat[cu.Name] = true
	}
	names := make([]string, 0, len(c.Optimizers))
	for n := range c.Optimizers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if !optimizerName.MatchString(n) {
			add("optimizers.%q is not a valid optimizer name; use lowercase letters, digits, '-' or '_'.", n)
		}
		switch c.Optimizers[n] {
		case StateOff, StateShadow, StateOn:
		default:
			add("optimizers.%s is %q; use \"off\", \"shadow\" or \"on\".", n, c.Optimizers[n])
		}
	}
	for _, a := range c.Optimize.CustomAgents {
		if a != "sdk" && a != "other" {
			add("optimize.custom_agents contains %q; only \"sdk\" and \"other\" can be marked custom. Claude Code and Codex manage their own prompts and caching.", a)
		}
	}
	if c.Optimize.BudgetMS < 1 || c.Optimize.BudgetMS > 1000 {
		add("optimize.budget_ms is %d; use a number of milliseconds from 1 to 1000 (default 25).", c.Optimize.BudgetMS)
	}
	if err := c.Optimize.CacheTTL.Check(); err != nil {
		add("%s.", err.Error())
	}
	if v := c.Optimize.Verbosity; v != VerbosityConcise && v != VerbosityTerse {
		add("optimize.verbosity is %q; use \"concise\" or \"terse\" (default \"concise\").", v)
	}
	if v := c.Optimize.ColdResumeAction; v != ColdResumeWarn && v != ColdResumeAsk {
		add("optimize.cold_resume_action is %q; use \"warn\" or \"ask\" (default \"warn\").", v)
	}
	if v := c.Optimize.ColdResumeMinTokens; v < 10000 || v > 2000000 {
		add("optimize.cold_resume_min_tokens is %d; use a number of tokens from 10000 to 2000000 (default 150000).", v)
	}
	if v := c.Optimize.ColdResumeMinWeekPct; !(v > 0 && v <= 100) {
		add("optimize.cold_resume_min_week_pct is %v; use a percentage above 0 and at most 100 (default 3).", v)
	}
	if v := c.Optimize.RouteTo; len(v) > 100 || strings.ContainsAny(v, " \t\r\n\"") {
		add("optimize.route_to is not a model name; use a model ID such as the one your agent sends, or \"\" to leave model_router idle.")
	}
	f := c.Fixer
	if f.MinSessions < 3 || f.MinSessions > 1000 {
		add("fixer.min_sessions is %d; use a number from 3 to 1000 (default 5).", f.MinSessions)
	}
	if f.LookbackDays < 1 || f.LookbackDays > 365 {
		add("fixer.lookback_days is %d; use a number of days from 1 to 365 (default 30).", f.LookbackDays)
	}
	if f.ContextMinTokens < 100 || f.ContextMinTokens > 100000 {
		add("fixer.context_min_tokens is %d; use a number from 100 to 100000 (default 1500).", f.ContextMinTokens)
	}
	if f.ActivityMinutes < 1 || f.ActivityMinutes > 1440 {
		add("fixer.activity_minutes is %d; use a number of minutes from 1 to 1440 (default 30).", f.ActivityMinutes)
	}
	if f.RetypedMinCount < 2 || f.RetypedMinCount > 1000 {
		add("fixer.retyped_min_count is %d; use a number from 2 to 1000 (default 5).", f.RetypedMinCount)
	}
	if f.RetypedMinSessions < 1 || f.RetypedMinSessions > 1000 {
		add("fixer.retyped_min_sessions is %d; use a number from 1 to 1000 (default 3).", f.RetypedMinSessions)
	}
	if t := c.Settings.DefaultTask; t != "" && !validTaskName(t) {
		add("settings.default_task is %q; use lowercase letters, digits and - (for example quick-fix), or leave it empty.", t)
	}
	if t := c.Tasks; t.MaxTurns < 0 || t.MaxTurns > 200 {
		add("tasks.max_turns is %d; use a number from 1 to 200, or 0 for the default (25).", t.MaxTurns)
	}
	if t := c.Tasks.TestCmd; strings.ContainsAny(t, "\r\n`") || len(t) > 200 {
		add("tasks.test_cmd must be one line of at most 200 characters without backticks.")
	}
	if c.Recovery.MaxMB < 1 || c.Recovery.MaxMB > 10000 {
		add("recovery.max_mb is %d; use a size in MB from 1 to 10000 (default 200).", c.Recovery.MaxMB)
	}
	if c.Recovery.TTLDays < 1 || c.Recovery.TTLDays > 365 {
		add("recovery.ttl_days is %d; use a number of days from 1 to 365 (default 7).", c.Recovery.TTLDays)
	}
	switch c.History.Plan {
	case "", "api_key", "subscription":
	default:
		add("history.plan is %q; use \"api_key\", \"subscription\" or leave it empty.", c.History.Plan)
	}
	switch c.Budget.Period {
	case "daily", "weekly", "monthly":
	default:
		add("budget.period is %q; use \"daily\", \"weekly\" or \"monthly\".", c.Budget.Period)
	}
	if c.Budget.CapUSD < 0 {
		add("budget.cap_usd is %g; use 0 for no cap or a positive amount.", c.Budget.CapUSD)
	}
	if c.Budget.UsageUSD < 0 {
		add("budget.usage_usd is %g; use 0 for no usage budget or a positive amount.", c.Budget.UsageUSD)
	}
	if c.Budget.WarnAtPercent < 1 || c.Budget.WarnAtPercent > 100 {
		add("budget.warn_at_percent is %d; use a number from 1 to 100.", c.Budget.WarnAtPercent)
	}
	m := c.Measure
	if m.BaselineDays < 0 || m.BaselineDays > 365 {
		add("measure.baseline_days is %d; use a number of days from 0 to 365 (default 7).", m.BaselineDays)
	}
	if m.BaselineSessions < 0 || m.BaselineSessions > 10000 {
		add("measure.baseline_sessions is %d; use a number from 0 to 10000 (default 20).", m.BaselineSessions)
	}
	if m.GitSignals && m.GitRepo == "" {
		add("measure.git_signals is on but measure.git_repo is empty; set git_repo to the folder of the repository to count commits in, or turn git_signals off.")
	}
	if m.MinSessions < 5 || m.MinSessions > 10000 {
		add("measure.min_sessions is %d; use a number from 5 to 10000 (default 20). Fewer than 5 cannot support any comparison.", m.MinSessions)
	}
	if m.RetryIncreasePercent < 1 || m.RetryIncreasePercent > 1000 {
		add("measure.retry_increase_percent is %d; use a number from 1 to 1000 (default 30).", m.RetryIncreasePercent)
	}
	if m.RefetchRatePercent < 1 || m.RefetchRatePercent > 100 {
		add("measure.refetch_rate_percent is %d; use a number from 1 to 100 (default 25).", m.RefetchRatePercent)
	}
	x := c.Experiment
	if x.MinSessionsPerArm < 5 || x.MinSessionsPerArm > 10000 {
		add("experiment.min_sessions_per_arm is %d; use a number from 5 to 10000 (default 20). Fewer than 5 cannot support any comparison.", x.MinSessionsPerArm)
	}
	if x.CostIncreasePercent < 1 || x.CostIncreasePercent > 1000 {
		add("experiment.cost_increase_percent is %d; use a number from 1 to 1000 (default 5).", x.CostIncreasePercent)
	}
	if x.OutcomeWorsenPercent < 1 || x.OutcomeWorsenPercent > 1000 {
		add("experiment.outcome_worsen_percent is %d; use a number from 1 to 1000 (default 20).", x.OutcomeWorsenPercent)
	}
	if problem := checkEndpoint(c.Contribute.Endpoint); problem != "" {
		add("contribute.endpoint %s", problem)
	}
	if c.RetentionDays < 1 || c.RetentionDays > 3650 {
		add("retention_days is %d; use a number of days from 1 to 3650.", c.RetentionDays)
	}
	return errors.Join(errs...)
}

// checkUpstream returns a plain-English problem with the URL, or "" if it is fine.
func checkUpstream(v string) string {
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("is %q; use a full URL such as \"https://api.example.com\".", v)
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return fmt.Sprintf("is %q; use https:// (plain http is only allowed for localhost).", v)
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Sprintf("is %q; give the origin only, without a path, because the gateway forwards each request's own path.", v)
	}
	// The gateway sends the user's API key to this host, so the URL must say
	// exactly one thing: where. Credentials, a query or a fragment in it are
	// either a mistake or an attempt to smuggle something along.
	if u.User != nil {
		return "contains a username or password; remove it. API keys come from your agent's own request, never from this setting."
	}
	if u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Sprintf("is %q; give the origin only, without a query or fragment.", v)
	}
	return ""
}

// checkEndpoint returns a plain-English problem with the upload URL, or "" if it
// is empty or fine. A path is allowed here, unlike an upstream origin.
func checkEndpoint(v string) string {
	if v == "" {
		return ""
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return fmt.Sprintf("is %q; use a full URL such as \"https://example.com/v1/contribute\".", v)
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
		return fmt.Sprintf("is %q; use https:// (plain http is only allowed for localhost).", v)
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Sprintf("is %q; give a plain URL without a username, query or fragment.", v)
	}
	return ""
}

// ValidateEndpoint returns "" if v is an acceptable upload URL, otherwise a
// plain-English reason. The uploader calls it too.
func ValidateEndpoint(v string) string { return checkEndpoint(v) }

// ValidateUpstream returns "" if v is an acceptable upstream origin, otherwise a
// plain-English reason. The gateway calls it too, so a server built without
// going through Load cannot be pointed somewhere unchecked.
func ValidateUpstream(v string) string { return checkUpstream(v) }

// ResolveDataDir picks the data directory: flag, then TROVE_DATA_DIR, then ~/.trove.
func ResolveDataDir(flag string, getenv func(string) string) (string, error) {
	if flag != "" {
		return filepath.Abs(flag)
	}
	if v := getenv("TROVE_DATA_DIR"); v != "" {
		return filepath.Abs(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", errors.New("cannot find your home directory; pass --data-dir or set TROVE_DATA_DIR")
	}
	return filepath.Join(home, ".trove"), nil
}

// Load reads <dataDir>/config.toml (defaults if absent), applies TROVE_
// environment overrides, and validates the result.
func Load(dataDir string, getenv func(string) string) (Config, error) {
	cfg := Default()
	path := filepath.Join(dataDir, FileName)
	b, err := os.ReadFile(path) //nolint:gosec // path is inside the user's own data directory
	switch {
	case err == nil:
		dec := toml.NewDecoder(strings.NewReader(string(b))).DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			var de *toml.DecodeError
			var sm *toml.StrictMissingError
			switch {
			case errors.As(err, &sm):
				return cfg, fmt.Errorf("%s has an unknown setting:\n%s\nCheck the spelling of that setting name, or delete the line.", path, sm.String())
			case errors.As(err, &de):
				row, col := de.Position()
				return cfg, fmt.Errorf("%s is not valid at line %d, column %d: %s. Fix that line or delete the file to use defaults.", path, row, col, de.Error())
			}
			return cfg, fmt.Errorf("cannot read %s: %v. Fix the file or delete it to use defaults.", path, err)
		}
		if cfg.Optimizers == nil {
			cfg.Optimizers = map[string]string{}
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return cfg, fmt.Errorf("cannot read %s: %v. Check its permissions.", path, err)
	}

	if err := applyEnv(&cfg, getenv); err != nil {
		return cfg, err
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid configuration in %s (or TROVE_ environment variables):\n%w", path, err)
	}
	// Autopilot only ever supplies states for optimizers the user left unset. A damaged
	// file means no Autopilot, never a failure to start.
	if f, aerr := LoadAutopilot(dataDir); aerr == nil {
		cfg = cfg.WithAutopilot(f)
	}
	return cfg, nil
}

func applyEnv(c *Config, getenv func(string) string) error {
	intVar := func(name string, dst *int) error {
		if v := getenv(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s is %q; it must be a whole number.", name, v)
			}
			*dst = n
		}
		return nil
	}
	strVar := func(name string, dst *string) {
		if v := getenv(name); v != "" {
			*dst = v
		}
	}
	if err := intVar("TROVE_GATEWAY_PORT", &c.Gateway.Port); err != nil {
		return err
	}
	if err := intVar("TROVE_GATEWAY_IDLE_MINUTES", &c.Gateway.IdleMinutes); err != nil {
		return err
	}
	if err := intVar("TROVE_RETENTION_DAYS", &c.RetentionDays); err != nil {
		return err
	}
	if err := intVar("TROVE_BUDGET_WARN_AT_PERCENT", &c.Budget.WarnAtPercent); err != nil {
		return err
	}
	strVar("TROVE_UPSTREAMS_ANTHROPIC", &c.Upstreams.Anthropic)
	strVar("TROVE_UPSTREAMS_OPENAI", &c.Upstreams.OpenAI)
	strVar("TROVE_UPSTREAMS_VERTEX", &c.Upstreams.Vertex)
	strVar("TROVE_UPSTREAMS_FOUNDRY", &c.Upstreams.Foundry)
	strVar("TROVE_UPSTREAMS_BEDROCK", &c.Upstreams.Bedrock)
	strVar("TROVE_BUDGET_PERIOD", &c.Budget.Period)
	if v := getenv("TROVE_NOTIFICATIONS_DESKTOP"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("TROVE_NOTIFICATIONS_DESKTOP is %q; use true or false.", v)
		}
		c.Notifications.Desktop = b
	}
	if v := getenv("TROVE_BUDGET_CAP_USD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("TROVE_BUDGET_CAP_USD is %q; it must be a number such as 25 or 12.5.", v)
		}
		c.Budget.CapUSD = f
	}
	if v := getenv("TROVE_BUDGET_USAGE_USD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("TROVE_BUDGET_USAGE_USD is %q; it must be a number such as 200 or 12.5.", v)
		}
		c.Budget.UsageUSD = f
	}
	return nil
}

// Recovery holds the limits of the local recovery store, where the full text of
// shortened tool output is kept so the agent can fetch it back (see docs/mcp.md).
type Recovery struct {
	// MaxMB caps the store's total size. The oldest outputs go first.
	MaxMB int `toml:"max_mb"`
	// TTLDays is how long an output is kept.
	TTLDays int `toml:"ttl_days"`
}

// validTaskName accepts the shape of a task type name. Which names exist is
// decided by the settings profiles, not here.
func validTaskName(s string) bool {
	if len(s) > 32 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

// Tasks holds the settings for `trove tasks` (the scout subagent and the
// cascade workflow, see docs/tasks.md). Empty values take the defaults in
// internal/tasks; whether a model name is one the agent documents is checked
// there, against the verified catalog, when a plan is made.
type Tasks struct {
	ScoutModel      string `toml:"scout_model"`
	WorkerModel     string `toml:"worker_model"`
	EscalationModel string `toml:"escalation_model"`
	// TestCmd is the project's test command. The cascade will not install
	// without it: escalating on a guess is not allowed.
	TestCmd string `toml:"test_cmd"`
	// MaxTurns limits the cheap worker before it must stop (0 means 25).
	MaxTurns int `toml:"max_turns"`
}
