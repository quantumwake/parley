//go:build !windows

package plugin

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// stopSessionWait ends the wait that holds this session, when its command
// line is parley wait. A recycled pid is left alone.
func stopSessionWait(env Env) error {
	w, ok := readWaitState(waitFile(env))
	if !ok || !processAlive(w.PID) || !waitCommand(w.PID) {
		return nil
	}

	p, err := os.FindProcess(w.PID)
	if err != nil {
		return nil
	}

	return p.Signal(syscall.SIGTERM)
}

func waitCommand(pid int) bool {
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}

	s := string(out)
	return strings.Contains(s, "parley") && strings.Contains(s, "wait")
}
