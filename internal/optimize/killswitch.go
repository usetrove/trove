package optimize

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/usetrove/trove/internal/config"
)

// KillFileName is created in the data directory by `trove optimize off`.
const KillFileName = "optimize.off"

// KillFile returns the kill-switch file path for a data directory.
func KillFile(dataDir string) string { return filepath.Join(dataDir, KillFileName) }

// Disabled reports whether the global kill switch is on, and why. It is checked
// on every request, so turning it on takes effect on the next one. Turning it on
// mid-session changes request bytes, which costs the provider's cache once; that
// is the intended trade for an instant stop.
func Disabled(killFile string, getenv func(string) string) (bool, string) {
	if getenv != nil {
		switch strings.ToLower(strings.TrimSpace(getenv(config.EnvOptimize))) {
		case "off", "0", "false", "disabled", "no":
			return true, "the " + config.EnvOptimize + " environment variable is set to off"
		}
	}
	if killFile != "" {
		if _, err := os.Stat(killFile); err == nil {
			return true, "`trove optimize off` is in effect"
		}
	}
	return false, ""
}
