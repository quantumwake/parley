//go:build linux

package plugin

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}

	var uid int
	var out error
	err = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if err != nil {
			out = err
			return
		}

		uid = int(cred.Uid)
	})
	if err != nil {
		return -1, err
	}

	return uid, out
}
