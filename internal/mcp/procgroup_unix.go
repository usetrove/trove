//go:build !windows

package mcp

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the command in its own process group and makes
// cancellation kill the whole group, so a timed-out test run leaves nothing
// behind.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
