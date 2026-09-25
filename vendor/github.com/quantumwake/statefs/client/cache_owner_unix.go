//go:build !windows

package client

import (
	"os"
	"syscall"
)

// ownedByUs reports whether the directory belongs to the user running
// this process. A private directory owned by somebody else is still
// somebody else's: they can replace what is in it.
func ownedByUs(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}

	return int(st.Uid) == os.Getuid()
}
