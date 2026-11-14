package hooks

import (
	"io"
	"os"
	"strings"

	"github.com/usetrove/trove/internal/setup"
)

// maxSettingsRead bounds how much of an agent settings file is read to look for Trove's hook. Settings files are
// a few KB; only the first MiB is read, so a larger file can only make the guardrail skip.
const maxSettingsRead = 1 << 20

// AdoptionObservable says whether the hook that records the agent's use of trove_run (post_tool_run) is installed
// in Claude Code, from the files Trove itself added hooks to (its manifest). hook_large_output's guardrail needs
// it: a hint that nobody could be seen following would look ignored, and the optimizer would be rolled back for
// the wrong reason. A hooks install from before this hook existed has to be refreshed with `trove hooks install`.
// It only reads, never writes, and any error means "not observable".
func AdoptionObservable(dataDir string) bool {
	changes, err := setup.HooksInstalled(dataDir)
	if err != nil {
		return false
	}
	for _, c := range changes {
		if c.Agent != setup.ClaudeHooks {
			continue
		}
		f, err := os.Open(c.Path) //nolint:gosec // a settings file Trove's own manifest says it edited
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(f, maxSettingsRead))
		_ = f.Close()
		if strings.Contains(string(b), EventPostRun) {
			return true
		}
	}
	return false
}
