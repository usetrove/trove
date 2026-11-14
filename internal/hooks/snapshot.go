package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/usetrove/trove/internal/config"
	"github.com/usetrove/trove/internal/optimize"
)

// Optimizer states, the same words config.toml uses.
const (
	stateOff    = "off"
	stateShadow = "shadow"
	stateOn     = "on"
)

// Snapshot is what a hook needs from config.toml and the guardrails, in a small
// JSON file it can read without opening the database or parsing the whole
// config. `trove hooks install`, the session-end hook and `trove hooks sync`
// rewrite it. A missing or damaged snapshot means every hook is off: doing
// nothing is always safe.
type Snapshot struct {
	V          int               `json:"v"`
	States     map[string]string `json:"states"`
	RolledBack []string          `json:"rolled_back"`
	// TroveRun is true when Trove's MCP server is registered, so trove_run exists
	// for the agent to use.
	TroveRun  bool  `json:"trove_run"`
	UpdatedAt int64 `json:"updated_at"`
	// RecoveryMaxMB and RecoveryTTLDays size the recovery store the output
	// trimmer writes to (config [recovery]); zero means the defaults.
	RecoveryMaxMB   int `json:"recovery_max_mb,omitempty"`
	RecoveryTTLDays int `json:"recovery_ttl_days,omitempty"`
	// RepoSalt and Tuning carry per-repo optimizer tuning (internal/tuning). A
	// session that starts in a repo pins that repo's applied values; the repo is
	// found by hashing the hook's working directory under RepoSalt, so no path is
	// stored here. Tuning is keyed by that hash.
	RepoSalt string `json:"repo_salt,omitempty"`
	// Trial lists the optimizers Autopilot has on trial. A session that starts now is assigned an arm for each
	// by a hash of its key (see trialArm): on for about half, shadow for the rest, and the choice is recorded.
	Trial  []string                   `json:"trial,omitempty"`
	Tuning map[string]optimize.Tuning `json:"tuning,omitempty"`
}

const snapshotVersion = 1

func snapshotPath(dir string) string { return filepath.Join(dir, "hooks", "config.json") }

// LoadSnapshot reads the snapshot. ok is false when it is missing, unreadable or
// from a newer version, in which case the zero Snapshot (everything off) is returned.
func LoadSnapshot(dir string) (Snapshot, bool) {
	b, err := os.ReadFile(snapshotPath(dir)) //nolint:gosec // path under Trove's data dir
	if err != nil {
		return Snapshot{}, false
	}
	var s Snapshot
	if json.Unmarshal(b, &s) != nil || s.V != snapshotVersion {
		return Snapshot{}, false
	}
	return s, true
}

// WriteSnapshot stores s atomically.
func WriteSnapshot(dir string, s Snapshot) error {
	s.V = snapshotVersion
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(snapshotPath(dir), b)
}

// BuildSnapshot derives the snapshot from the config, the guardrail rollbacks
// (measure.Overrides) and whether the MCP server is registered.
func BuildSnapshot(cfg config.Config, rolledBack map[string]string, troveRun bool, now time.Time) Snapshot {
	s := Snapshot{V: snapshotVersion, States: map[string]string{}, RolledBack: []string{}, TroveRun: troveRun, UpdatedAt: now.Unix(),
		RecoveryMaxMB: cfg.Recovery.MaxMB, RecoveryTTLDays: cfg.Recovery.TTLDays, Trial: hookTrials(cfg)}
	for _, n := range OptimizerNames {
		s.States[n] = cfg.OptimizerState(n)
		if _, ok := rolledBack[n]; ok {
			s.RolledBack = append(s.RolledBack, n)
		}
	}
	return s
}

// OptimizerNames are the optimizers hooks implement.
var OptimizerNames = []string{
	config.OptimizerHookRepeatRead, config.OptimizerHookLargeOutput,
	config.OptimizerHookReadLimit, config.OptimizerHookQuietCommands,
	config.OptimizerNoiseNormalizer, config.OptimizerOutputTrimmer,
}

// State is the state a hook acts in right now for the named optimizer. pinned
// is the state recorded when the session started ("" if none was: the session
// began before the hooks were installed, so it stays off). A session keeps the
// state it started with, so a change in config.toml waits for the next session
// (CLAUDE.md rule 5). The kill switch and a guardrail rollback only ever lower
// it, and they take effect at once.
func (s Snapshot) State(name, pinned, dataDir string, getenv func(string) string) string {
	if off, _ := optimize.Disabled(optimize.KillFile(dataDir), getenv); off {
		return stateOff
	}
	return s.lowered(name, pinned)
}

func (s Snapshot) lowered(name, cur string) string {
	if cur == stateOn {
		for _, n := range s.RolledBack {
			if n == name {
				return stateShadow
			}
		}
	}
	switch cur {
	case stateOn, stateShadow:
		return cur
	}
	return stateOff
}

// Pin is the state a session that starts now keeps: config.toml, lowered by any
// rollback.
func (s Snapshot) Pin(name string) string { return s.lowered(name, s.States[name]) }

// hookTrials is the part of cfg's trial list the hooks implement.
func hookTrials(cfg config.Config) []string {
	var out []string
	for _, n := range cfg.AutopilotTrials() {
		for _, h := range OptimizerNames {
			if h == n {
				out = append(out, n)
				break
			}
		}
	}
	return out
}
