package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// AutopilotFileName is the Autopilot consent record and state, kept in the data
// directory next to contribute.json. It is written only by `trove autopilot`,
// `trove setup` (after the user agrees) and Autopilot's own session-boundary
// evaluation. See docs/autopilot.md.
const AutopilotFileName = "autopilot.json"

// AutopilotPolicy is the one policy this version knows. A file naming another
// policy is ignored, so an older program never applies rules it does not know.
const AutopilotPolicy = "lossless-v1"

// Autopilot states. Only AutopilotOn raises an optimizer above shadow.
const (
	AutopilotOff        = "off"
	AutopilotShadow     = "shadow"
	AutopilotCandidate  = "candidate"
	AutopilotOn         = "on"
	AutopilotRolledBack = "rolled_back"
)

// AutopilotEntry is Autopilot's state for one optimizer. Names, states, times
// and counters only.
type AutopilotEntry struct {
	State    string `json:"state"`
	SinceMS  int64  `json:"since_ms"`
	Reason   string `json:"reason,omitempty"`   // plain English, never request content
	Attempts int    `json:"attempts,omitempty"` // times it was promoted to on
}

// AutopilotFile is the contents of autopilot.json.
type AutopilotFile struct {
	Version      int                       `json:"version"`
	Enabled      bool                      `json:"enabled"`
	Paused       bool                      `json:"paused"`
	Policy       string                    `json:"policy"`
	ConsentedAt  int64                     `json:"consented_at_ms"`
	Entries      map[string]AutopilotEntry `json:"entries"`
	LastEvalMS   int64                     `json:"last_eval_ms,omitempty"`
	LastEvalNote string                    `json:"last_eval_note,omitempty"`
}

// Active reports whether Autopilot may influence optimizer states: the user
// consented, it is not paused, and the policy is one this version understands.
func (f AutopilotFile) Active() bool {
	return f.Enabled && !f.Paused && f.Policy == AutopilotPolicy
}

// States maps Autopilot's entries to the configured state an optimizer gets for
// new sessions. Only "on" is on; shadow, candidate and rolled_back all run in
// shadow so evidence keeps building; anything else is off.
func (f AutopilotFile) States() map[string]string {
	if !f.Active() {
		return nil
	}
	out := make(map[string]string, len(f.Entries))
	for name, e := range f.Entries {
		switch e.State {
		case AutopilotOn:
			out[name] = StateOn
		case AutopilotShadow, AutopilotCandidate, AutopilotRolledBack:
			out[name] = StateShadow
		}
	}
	return out
}

// Names returns the optimizers in the file, sorted.
func (f AutopilotFile) Names() []string {
	n := make([]string, 0, len(f.Entries))
	for k := range f.Entries {
		n = append(n, k)
	}
	sort.Strings(n)
	return n
}

// LoadAutopilot reads autopilot.json. A missing file is the zero value (not
// enabled). A damaged file is an error and the caller treats it as "not enabled":
// every failure here lowers behavior to "nothing changes".
func LoadAutopilot(dataDir string) (AutopilotFile, error) {
	p := filepath.Join(dataDir, AutopilotFileName)
	b, err := os.ReadFile(p) //nolint:gosec // inside the user's own data directory
	if errors.Is(err, os.ErrNotExist) {
		return AutopilotFile{Version: 1, Entries: map[string]AutopilotEntry{}}, nil
	}
	if err != nil {
		return AutopilotFile{}, fmt.Errorf("cannot read %s: %v. Check its permissions, or run `trove autopilot off`.", p, err)
	}
	var f AutopilotFile
	if err := json.Unmarshal(b, &f); err != nil {
		return AutopilotFile{}, fmt.Errorf("%s is damaged (%v). Run `trove autopilot off` to remove it, then `trove setup` to start again.", p, err)
	}
	if f.Entries == nil {
		f.Entries = map[string]AutopilotEntry{}
	}
	return f, nil
}

// SaveAutopilot writes autopilot.json atomically with permissions 0600.
func SaveAutopilot(dataDir string, f AutopilotFile) error {
	f.Version = 1
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	p := filepath.Join(dataDir, AutopilotFileName)
	tmp, err := os.CreateTemp(dataDir, AutopilotFileName+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(name, 0o600)
	return os.Rename(name, p)
}

// WithAutopilot returns c with Autopilot's states applied under the user's own
// settings: an optimizer named in config.toml or a TROVE_ variable keeps the
// user's state, whatever Autopilot would choose.
func (c Config) WithAutopilot(f AutopilotFile) Config {
	c.autopilot = f.States()
	return c
}

// AutopilotStates returns the states Autopilot contributes to this Config.
func (c Config) AutopilotStates() map[string]string { return c.autopilot }

// WithAutopilotStates returns c with the given Autopilot states in place of any it
// had, for a long-running process that re-reads autopilot.json as sessions start.
func (c Config) WithAutopilotStates(states map[string]string) Config {
	c.autopilot = states
	return c
}
