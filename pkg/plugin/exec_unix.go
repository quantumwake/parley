//go:build !windows

package plugin

import "syscall"

// waitExec replaces this process with another program. Tests stand in for
// it; on success the real one does not return.
var waitExec = syscall.Exec
