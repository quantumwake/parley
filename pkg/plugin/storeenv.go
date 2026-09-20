package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

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
		return adapter.New(adapter.Config{Directory: env.Directory, Credentials: sfs.Credentials{KeyFile: env.IdentityPath, Tenant: env.Tenant}}), nil
	}

	return nil, errors.New("no store configured: set STATEFS_DIRECTORY or STATEFS_AI_STORE=file:<dir>")
}

// SpoolDir is where sessions are spooled.
func SpoolDir(env Env) string { return filepath.Join(env.DataDir, "spool") }
