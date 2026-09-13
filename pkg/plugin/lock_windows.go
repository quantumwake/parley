//go:build windows

package plugin

import (
	"errors"
	"os"
	"syscall"
)

// tryLock opens path with no sharing, which Windows refuses to a second
// opener until the handle closes; the handle closes when the process
// exits, so a crashed daemon never leaves it held.
func tryLock(path string) (*os.File, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}

	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil, syscall.OPEN_ALWAYS, syscall.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		const errorSharingViolation = syscall.Errno(32)
		if errors.Is(err, errorSharingViolation) {
			return nil, errLocked
		}

		return nil, err
	}

	return os.NewFile(uintptr(h), path), nil
}
