//go:build darwin

package plugin

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID is the uid of the process on the other end of a unix connection,
// from the kernel. It is who is answering, or who connected, not who
// created the socket file.
func peerUID(conn *net.UnixConn) (int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return -1, err
	}

	var uid int
	var out error
	err = raw.Control(func(fd uintptr) {
		cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
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
