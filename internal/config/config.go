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
	Budget        Budget            `toml:"budget"`
	RetentionDays int               `toml:"retention_days"`
}

// Gateway holds local gateway settings. It always binds to 127.0.0.1.
type Gateway struct {
	Port        int `toml:"port"`
	IdleMinutes int `toml:"idle_minutes"`
}

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
		Gateway:       Gateway{Port: 8787, IdleMinutes: 30},
		Upstreams:     Upstreams{Anthropic: "https://api.anthropic.com", OpenAI: "https://api.openai.com"},
		Optimizers:    map[string]string{},
		Budget:        Budget{Period: "monthly", CapUSD: 0, WarnAtPercent: 80},
		RetentionDays: 90,
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

[budget]
# Budget period: "daily", "weekly" or "monthly".
period = "monthly"
# Spending cap in USD for the period. 0 means no cap.
cap_usd = 0.0
# Warn when this percent of the cap is used (1 to 100).
warn_at_percent = 80
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
	return ""
}

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
	if v := getenv("TROVE_BUDGET_CAP_USD"); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return fmt.Errorf("TROVE_BUDGET_CAP_USD is %q; it must be a number such as 25 or 12.5.", v)
		}
		c.Budget.CapUSD = f
	}
	return nil
}
