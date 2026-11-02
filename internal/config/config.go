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
	Notifications Notifications     `toml:"notifications"`
	History       History           `toml:"history"`
	Fixer         Fixer             `toml:"fixer"`
	Recovery      Recovery          `toml:"recovery"`
	Contribute    Contribute        `toml:"contribute"`
	RetentionDays int               `toml:"retention_days"`
}

// Contribute holds the setting for the optional anonymous summary upload (see
// PRIVACY.md). Whether upload is switched on is not here: it is a consent record
// kept in contribute.json by `trove contribute enable`.
type Contribute struct {
	// Endpoint is the HTTPS URL that receives the summary. Empty means upload is
	// not possible, whatever the consent record says.
	Endpoint string `toml:"endpoint"`
}

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

// Gateway holds local gateway settings. It always binds to 127.0.0.1.
type Gateway struct {
	Port        int `toml:"port"`
	IdleMinutes int `toml:"idle_minutes"`
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
)

// EnvOptimize is the kill-switch environment variable. Set to "off", it
// disables every transformation immediately.
const EnvOptimize = "TROVE_OPTIMIZE"

// Upstreams are the provider origins the gateway forwards to.
type Upstreams struct {
	Anthropic string `toml:"anthropic"`
	OpenAI    string `toml:"openai"`
}

// Budget holds spending-cap settings. A CapUSD of 0 means no cap.
type Budget struct {
	Period        string  `toml:"period"`
	CapUSD        float64 `toml:"cap_usd"`
	WarnAtPercent int     `toml:"warn_at_percent"`
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Gateway:    Gateway{Port: 8787, IdleMinutes: 30},
		Upstreams:  Upstreams{Anthropic: "https://api.anthropic.com", OpenAI: "https://api.openai.com"},
		Optimizers: map[string]string{},
		Optimize:   Optimize{CustomAgents: []string{}, BudgetMS: 25},
		Budget:     Budget{Period: "monthly", CapUSD: 0, WarnAtPercent: 80},
		Measure: Measure{
			BaselineDays: 7, BaselineSessions: 20, MinSessions: 20,
			RetryIncreasePercent: 30, RefetchRatePercent: 25,
		},
		RetentionDays: 90,
		History:       History{Enabled: true},
		Fixer:         Fixer{MinSessions: 5, LookbackDays: 30, ContextMinTokens: 1500, ActivityMinutes: 30},
		Recovery:      Recovery{MaxMB: 200, TTLDays: 7},
	}
}

// OptimizerState returns the state of the named optimizer; unlisted means off.
func (c Config) OptimizerState(name string) string {
	if s, ok := c.Optimizers[name]; ok {
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

[budget]
# Budget period: "daily", "weekly" or "monthly".
period = "monthly"
# Spending cap in USD for the period. 0 means no cap.
cap_usd = 0.0
# Warn when this percent of the cap is used (1 to 100).
warn_at_percent = 80

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
	for name, v := range map[string]string{"upstreams.anthropic": c.Upstreams.Anthropic, "upstreams.openai": c.Upstreams.OpenAI} {
		if problem := checkUpstream(v); problem != "" {
			add("%s %s", name, problem)
		}
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
				return cfg, fmt.Errorf("%s has an unknown setting:\n%s\nCheck the spelling against `trove config get`, or delete the line.", path, sm.String())
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
