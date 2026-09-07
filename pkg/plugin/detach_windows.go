//go:build windows

package plugin

import (
	"os"
	"os/exec"
	"syscall"
)

// detach starts the daemon in a new process group with no console so the
// hook's exit does not take it down.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008} // DETACHED_PROCESS
}

// processAlive reports whether a process with pid can be opened.
func processAlive(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Windows FindProcess succeeds only for a live process handle.
	_ = p.Release()
	return true
}
