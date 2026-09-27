//go:build !unix

package plugin

import (
	"context"
	"errors"

	"github.com/quantumwake/parley/pkg/store"
	adapter "github.com/quantumwake/parley/pkg/store/statefs"
)

// useRelay is never a socket on Windows. Commands connect directly.
func useRelay(string, string) (string, bool) {
	return "", false
}

// installRelay does not wrap the client. Windows has no session socket.
func installRelay(Env, *adapter.Store) {}

// startSessionRelay reports that the relay cannot listen here. The daemon
// ignores the error and keeps going, so a Windows session connects directly.
// The switch stays off by default on every system.
func startSessionRelay(_ context.Context, env Env, session string, _ store.Store) (func(), error) {
	if !Enabled(env, "daemon-relay") || session == "" {
		return nil, nil
	}

	return nil, errors.New("daemon relay is unsupported")
}
