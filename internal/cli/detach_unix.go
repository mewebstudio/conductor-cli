//go:build !windows

package cli

import (
	"os/exec"
	"syscall"
)

// detach runs cmd in its own session so it outlives the hook.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
