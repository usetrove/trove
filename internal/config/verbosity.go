package config

import (
	"fmt"
	"strings"
)

// VerbosityLearnedMark ends the optimize.verbosity line that `trove optimize verbosity
// --apply` writes, so a later run can tell its own value from one the user set by hand.
const VerbosityLearnedMark = "# learned by trove optimize verbosity"

// VerbositySetting is optimize.verbosity as written in config.toml.
type VerbositySetting struct {
	Value string `json:"value"`
	// Learned is true when the line was written by `trove optimize verbosity --apply`.
	Learned bool `json:"learned"`
	// HandSet is true when the user chose a non-default level themselves; a learned
	// level never replaces it without --force.
	HandSet bool `json:"hand_set"`
}

// ReadVerbositySetting reads optimize.verbosity from <dataDir>/config.toml without
// validating the rest of the file. A missing file or line is the default.
func ReadVerbositySetting(dataDir string) (VerbositySetting, error) {
	s := VerbositySetting{Value: VerbosityConcise}
	src, err := readConfigText(dataDir)
	if err != nil {
		return s, err
	}
	lines := strings.Split(strings.ReplaceAll(src, "\r\n", "\n"), "\n")
	start, end := sectionBounds(lines, "optimize")
	if start < 0 {
		return s, nil
	}
	i := keyLine(lines, start, end, "verbosity")
	if i < 0 {
		return s, nil
	}
	l := lines[i]
	s.Learned = strings.Contains(l, VerbosityLearnedMark)
	_, v, _ := strings.Cut(l, "=")
	if q := strings.SplitN(strings.TrimSpace(v), `"`, 3); len(q) == 3 {
		s.Value = q[1]
	}
	s.HandSet = !s.Learned && s.Value != VerbosityConcise
	return s, nil
}

// SetVerbosity writes optimize.verbosity = level to <dataDir>/config.toml, marked as
// learned. Only that line changes; it applies to new sessions.
func SetVerbosity(dataDir, level string) error {
	if level != VerbosityConcise && level != VerbosityTerse {
		return fmt.Errorf("verbosity level is %q; use %q or %q.", level, VerbosityConcise, VerbosityTerse)
	}
	line := fmt.Sprintf("verbosity = %q %s", level, VerbosityLearnedMark)
	return editConfigFile(dataDir, func(src string) string {
		return setSectionLines(src, "optimize", [][2]string{{"verbosity", line}})
	})
}
