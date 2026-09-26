package plugin

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// errAbstract is an abstract socket name. Those have no filesystem
// permissions, so none of the ownership checks would apply.
var errAbstract = errors.New("relay: abstract sockets are not used")

// relaySocket is the daemon's socket for one session. It lives under
// <data>/r/ and not under the session directory: that path is one byte
// from the platform's limit on this machine. The name is the first 16 hex
// digits of sha256(session), so the session id is not on the path.
func relaySocket(dataDir, session string) (string, bool) {
	if dataDir == "" || session == "" {
		return "", false
	}

	sum := sha256.Sum256([]byte(session))
	path := filepath.Join(dataDir, "r", hex.EncodeToString(sum[:8])+".sock")
	if len(path) > 103 || abstract(path) {
		return "", false
	}

	return path, true
}

func abstract(path string) bool {
	return path == "" || path[0] == '@' || path[0] == 0 || strings.ContainsRune(path, 0)
}

// listenUnix refuses an abstract name before the kernel sees it.
func listenUnix(path string) (net.Listener, error) {
	if abstract(path) {
		return nil, errAbstract
	}

	return net.Listen("unix", path)
}

// relayPathTrusted reports whether every component from dataDir through
// the socket is a real node we own and that nobody else can write. A
// symlink is refused: another uid could swap a directory for one between
// the check and the connect. The socket itself must be a socket.
func relayPathTrusted(dataDir, sock string) bool {
	if abstract(sock) {
		return false
	}

	rel, err := filepath.Rel(dataDir, sock)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}

	cur := dataDir
	parts := strings.Split(rel, string(os.PathSeparator))
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if err != nil || !oursAndTight(st) {
			return false
		}

		if st.Mode()&os.ModeSymlink != 0 {
			return false
		}

		last := i == len(parts)-1
		if last && st.Mode()&os.ModeSocket == 0 {
			return false
		}
	}

	// The data directory itself is an ancestor and must hold the same rule.
	st, err := os.Lstat(dataDir)
	if err != nil || !oursAndTight(st) || st.Mode()&os.ModeSymlink != 0 {
		return false
	}

	return true
}

func oursAndTight(st os.FileInfo) bool {
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || int(sys.Uid) != os.Getuid() {
		return false
	}

	// Group- or world-writable: someone else can replace a component.
	return st.Mode().Perm()&0o022 == 0
}
