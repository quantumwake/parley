package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	sfs "github.com/quantumwake/statefs/client"

	"github.com/quantumwake/parley/pkg/store"
	adapter "github.com/quantumwake/parley/pkg/store/statefs"
)

// StoreFromEnv picks the store: STATEFS_AI_STORE=file:<dir> for offline
// runs, else the enrolled-directory adapter when STATEFS_DIRECTORY is set. The
// spool directory lives beside either so a session never depends on the
// store to be captured.
func StoreFromEnv(env Env) (store.Store, error) {
	if v := os.Getenv("STATEFS_AI_STORE"); strings.HasPrefix(v, "file:") {
		return store.NewFile(strings.TrimPrefix(v, "file:"))
	}

	if env.Directory != "" {
		// The identity comes from the resolved Env (environment, then the
		// config file), not from the SDK's own ladder, which would miss a
		// config-only setup.
		return adapter.New(adapter.Config{
			Directory:   env.Directory,
			Credentials: sfs.Credentials{KeyFile: env.IdentityPath, Tenant: env.Tenant},
			Cache:       storeCache(env),
		}), nil
	}

	return nil, errors.New("no store configured: set STATEFS_DIRECTORY or STATEFS_AI_STORE=file:<dir>")
}

// storeCache answers where this machine keeps the route and the ticket
// between commands, or nil when it has not opted in.
//
// Every parley command is its own process, and resolving a namespace and
// minting a ticket are two round trips to the directory — measured at
// ~0.5s each from a laptop, which is most of what a post costs. Keeping
// them is off by default like every optional path (features.go), and the
// two switches are separate because they are separate risks: a stale
// route is corrected by a 307, a stale ticket by a 401.
func storeCache(env Env) sfs.Cache {
	route, ticket := Enabled(env, "route-cache"), Enabled(env, "ticket-cache")
	if !route && !ticket {
		return nil
	}

	// The identity scopes the cache's own directory: two identities on one
	// machine must never share a ticket, which is also a MAC key.
	cache := sfs.NewDiskCache(filepath.Join(env.DataDir, "cache"), env.IdentityPath)
	if cache == nil {
		return nil
	}

	return partialCache{Cache: cache, route: route, ticket: ticket}
}

// partialCache honours one switch without the other: a key it is not
// allowed to keep is answered as absent and never written, so turning one
// on is exactly one behaviour change and not two.
type partialCache struct {
	sfs.Cache
	route  bool
	ticket bool
}

func (p partialCache) allowed(key string) bool {
	switch {
	case strings.HasPrefix(key, "route\x00"):
		return p.route
	case strings.HasPrefix(key, "ticket\x00"):
		return p.ticket
	}

	return false
}

func (p partialCache) Get(key string) ([]byte, bool) {
	if !p.allowed(key) {
		return nil, false
	}

	return p.Cache.Get(key)
}

func (p partialCache) Put(key string, value []byte, expires time.Time) {
	if !p.allowed(key) {
		return
	}

	p.Cache.Put(key, value, expires)
}

// SpoolDir is where sessions are spooled.
func SpoolDir(env Env) string { return filepath.Join(env.DataDir, "spool") }
