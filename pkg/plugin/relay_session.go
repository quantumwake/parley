package plugin

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/quantumwake/parley/pkg/store"
	adapter "github.com/quantumwake/parley/pkg/store/statefs"
)

// installRelay makes this command's client borrow the session daemon's
// connection when the switch is on. Off is the default, and then this
// changes nothing.
func installRelay(env Env, st *adapter.Store) {
	if !Enabled(env, "daemon-relay") || env.Session == "" || st == nil {
		return
	}

	c := st.Client()
	if c.HTTP == nil {
		c.HTTP = &http.Client{Timeout: 30 * time.Second}
	}

	var base http.RoundTripper
	if c.HTTP.Transport != nil {
		base = c.HTTP.Transport
	}

	c.HTTP.Transport = relayRoundTripper{base: base, dataDir: env.DataDir, session: env.Session}
}

// startSessionRelay listens for this session when the switch is on.
// A file store, a switch that is off, or a path that is not ours listens
// for nothing: commands then connect directly.
func startSessionRelay(ctx context.Context, env Env, session string, st store.Store) (func(), error) {
	if !Enabled(env, "daemon-relay") || session == "" {
		return nil, nil
	}

	if _, ok := st.(interface {
		RelayMember(ctx context.Context, ns string) (string, error)
	}); !ok {
		return nil, nil
	}

	path, ok := relaySocket(env.DataDir, session)
	if !ok {
		return nil, nil
	}

	info, err := os.Lstat(env.DataDir)
	if err != nil || !oursAndTight(info) {
		return nil, nil
	}

	ln, err := serveRelay(path, relayAllowFrom(st))
	if err != nil {
		return nil, err
	}

	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	return func() { ln.Close() }, nil
}

func relayAllowFrom(st store.Store) relayAllow {
	type member interface {
		RelayMember(ctx context.Context, ns string) (string, error)
	}

	rt, ok := st.(member)
	if !ok {
		return func(string) (string, bool) { return "", false }
	}

	return func(path string) (string, bool) {
		ns := namespaceOf(path)
		if ns == "" {
			return "", false
		}

		u, err := rt.RelayMember(context.Background(), ns)
		if err != nil || !strings.HasPrefix(u, "https://") {
			return "", false
		}

		return u, true
	}
}

func namespaceOf(path string) string {
	const prefix = "/api/v1/state/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}

	rest := path[len(prefix):]
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		return ""
	}

	return rest[:i]
}
