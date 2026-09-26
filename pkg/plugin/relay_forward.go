package plugin

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// relayAllow turns a request path into the upstream base URL. The URL is
// the daemon's, from the directory's allowlist. The request's Host is not
// an input.
type relayAllow func(path string) (base string, ok bool)

// relayClient does not follow redirects. A 307 belongs to the command,
// which is the side that knows a member moved.
var relayClient = &http.Client{
	Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// serveRelay listens on path and forwards each request allow accepts.
// The peer must be this uid. path must already be trusted by the caller
// that created the directory.
func serveRelay(path string, allow relayAllow) (net.Listener, error) {
	if err := os.MkdirAll(filepathDir(path), 0o700); err != nil {
		return nil, err
	}

	_ = os.Remove(path)
	ln, err := listenUnix(path)
	if err != nil {
		return nil, err
	}

	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}

	srv := &http.Server{
		Handler: relayHandler(allow),
		ConnContext: func(ctx context.Context, c net.Conn) context.Context {
			return context.WithValue(ctx, relayConnKey{}, c)
		},
	}
	go srv.Serve(ln)
	return ln, nil
}

func filepathDir(path string) string {
	i := strings.LastIndex(path, string(os.PathSeparator))
	if i < 0 {
		return "."
	}

	return path[:i]
}

func relayHandler(allow relayAllow) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !relayPeerOK(r) {
			http.Error(w, "relay: peer refused", http.StatusForbidden)
			return
		}

		base, ok := allow(r.URL.Path)
		if !ok || !strings.HasPrefix(base, "https://") {
			http.Error(w, "relay: not an allowed member", http.StatusForbidden)
			return
		}

		up, err := http.NewRequestWithContext(r.Context(), r.Method, strings.TrimRight(base, "/")+r.URL.RequestURI(), r.Body)
		if err != nil {
			http.Error(w, "relay: bad request", http.StatusBadRequest)
			return
		}

		for k, vs := range r.Header {
			if strings.EqualFold(k, "Host") {
				continue
			}

			for _, v := range vs {
				up.Header.Add(k, v)
			}
		}

		resp, err := relayClient.Do(up)
		if err != nil {
			http.Error(w, "relay: upstream", http.StatusBadGateway)
			return
		}

		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}

		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	})
}

// relayPeerOK is split out so a test can substitute the uid the kernel
// reports. The default reads the connection the HTTP server accepted.
var relayPeerOK = func(r *http.Request) bool {
	c, ok := connFrom(r)
	if !ok {
		return false
	}

	uid, err := peerUID(c)
	return err == nil && uid == os.Getuid()
}

func connFrom(r *http.Request) (*net.UnixConn, bool) {
	c, ok := r.Context().Value(relayConnKey{}).(net.Conn)
	if !ok {
		return nil, false
	}

	u, ok := c.(*net.UnixConn)
	return u, ok
}

type relayConnKey struct{}
