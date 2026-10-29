//go:build windows

package mcp

import "os/exec"

// setProcessGroup does nothing on Windows; the default cancellation kills the
// started process only.
func setProcessGroup(*exec.Cmd) {}
