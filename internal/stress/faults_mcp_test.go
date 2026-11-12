package stress

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildTrove builds the real trove binary for the MCP case, so the test starts the same server users run.
func buildTrove(t *testing.T) string {
	t.Helper()
	name := "trove"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	out := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "build", "-o", out, "../../cmd/trove")
	if msg, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("could not build trove for the MCP case: %v\n%s", err, msg)
	}
	return out
}

func TestDeadMCPServerFailsOpen(t *testing.T) {
	stressTroveBinary = buildTrove(t)
	t.Cleanup(func() { stressTroveBinary = "" })
	e := &env{dir: t.TempDir()}
	if vs := e.deadMCPFault(); len(vs) > 0 {
		t.Fatalf("dead MCP server did not fail open: %+v", vs)
	}
}
