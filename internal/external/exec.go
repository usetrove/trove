package external

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

// ExecRunner runs manifest commands directly, never through a shell. Trove
// itself makes no network calls: if the command (cargo, pip, brew) does, that is
// the package manager acting on the install the user just approved.
type ExecRunner struct {
	Timeout time.Duration // default 10 minutes
}

const maxOutput = 64 << 10

type capped struct{ b bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if c.b.Len() < maxOutput {
		room := maxOutput - c.b.Len()
		if len(p) > room {
			c.b.Write(p[:room])
		} else {
			c.b.Write(p)
		}
	}
	return len(p), nil
}

// Run executes argv and returns its combined, size-capped output.
func (r ExecRunner) Run(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 || argv[0] == "" {
		return "", errors.New("empty command")
	}
	d := r.Timeout
	if d == 0 {
		d = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // argv comes from an embedded manifest the user approved
	var out capped
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	return out.b.String(), err
}

// LookPath reports whether name is on PATH.
func (ExecRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }
