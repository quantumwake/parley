package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/quantumwake/statefs.ai/pkg/store"
	adapter "github.com/quantumwake/statefs.ai/pkg/store/statefs"
)

// StoreFromEnv picks the store: STATEFS_AI_STORE=file:<dir> for offline
// runs, else the statefs.io adapter when STATEFS_DIRECTORY is set. The
// spool directory lives beside either so a session never depends on the
// store to be captured.
func StoreFromEnv(env Env) (store.Store, error) {
	if v := os.Getenv("STATEFS_AI_STORE"); strings.HasPrefix(v, "file:") {
		return store.NewFile(strings.TrimPrefix(v, "file:"))
	}

	if env.Directory != "" {
		return adapter.New(adapter.Config{Directory: env.Directory}), nil
	}

	return nil, errors.New("no store configured: set STATEFS_DIRECTORY (statefs.io) or STATEFS_AI_STORE=file:<dir>")
}

// SpoolDir is where sessions are spooled.
func SpoolDir(env Env) string { return filepath.Join(env.DataDir, "spool") }
