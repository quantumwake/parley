//go:build !windows

package plugin

import (
	"os"
	"os/exec"
	"syscall"
)

// detach puts the daemon in its own session so Claude Code's exit does not
// take it down.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// ProcessAlive reports whether pid answers a null signal.
func ProcessAlive(pid int) bool { return processAlive(pid) }

// processAlive reports whether pid answers a null signal.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	return err == nil && p.Signal(syscall.Signal(0)) == nil
}
