//go:build !windows

package plugin

import (
	"errors"
	"os"
	"syscall"
)

// tryLock takes an exclusive, non-blocking lock on path. The kernel drops
// it when the process exits, so a crashed daemon never leaves it held.
func tryLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLocked
		}

		return nil, err
	}

	return f, nil
}
