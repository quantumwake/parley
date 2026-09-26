package plugin

import (
	"context"
	"net"
	"net/http"
	"os"
	"time"
)

// useRelay reports the socket to borrow, or false when the relay must not
// be used and the command should connect directly. A missing, untrusted,
// or too-long path is false: nothing has been sent.
func useRelay(dataDir, session string) (string, bool) {
	path, ok := relaySocket(dataDir, session)
	if !ok || !relayPathTrusted(dataDir, path) {
		return "", false
	}

	return path, true
}

// relayRoundTripper sends a request over the session's socket when that
// socket is trusted and accepts quickly. A failure before any byte is
// written connects directly. A failure after that is returned: an append
// must not be sent twice.
type relayRoundTripper struct {
	base    http.RoundTripper
	dataDir string
	session string
}

func (t relayRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}

	sock, ok := useRelay(t.dataDir, t.session)
	if !ok {
		return base.RoundTrip(req)
	}

	conn, err := net.DialTimeout("unix", sock, 200*time.Millisecond)
	if err != nil {
		return base.RoundTrip(req)
	}

	uc, ok := conn.(*net.UnixConn)
	uid, uidErr := 0, error(nil)
	if ok {
		uid, uidErr = peerUID(uc)
	}

	if !ok || uidErr != nil || uid != os.Getuid() {
		conn.Close()
		return base.RoundTrip(req)
	}

	tr := &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return conn, nil
		},
	}
	out := req.Clone(req.Context())
	out.URL.Scheme = "http"
	out.URL.Host = "parley-relay"
	resp, err := tr.RoundTrip(out)
	if err != nil {
		return nil, err
	}

	return resp, nil
}
