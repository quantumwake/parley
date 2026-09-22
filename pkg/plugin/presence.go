package plugin

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/quantumwake/parley/pkg/agentaccess"
	"github.com/quantumwake/statefs/pkg/identityfile"
)

// Presence pings are display-only. They must never sit on wait's path:
// a down statefs.ai must not change wait's exit, and must not slow a
// delivery. Owner: "parley should not break if it cannot communicate
// with the presence service.. nor should it block."

var (
	presenceMu   sync.Mutex
	presenceNext time.Time
	presenceGap  = 5 * time.Second

	// presenceSend is the ping. Tests replace it with a call that never
	// returns, which must not stall Wait.
	presenceSend = sendPresence
)

func sendPresence(ctx context.Context, env Env, state string) error {
	path := env.IdentityPath
	if path == "" {
		path = identityfile.DefaultPath()
	}
	f, err := identityfile.Read(path)
	if err != nil {
		return err
	}
	key, err := f.Private()
	if err != nil {
		return err
	}
	base := env.StatefsAI
	if base == "" {
		base = agentaccess.Base()
	}
	var names []string
	for _, s := range Subscriptions(env) {
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	c := &agentaccess.Client{
		Base: base, Username: f.Username, Key: key,
		HTTP: &http.Client{Timeout: 2 * time.Second}, UserAgent: UserAgent(),
	}
	return c.Presence(ctx, env.Session, state, names)
}

func touchPresence(env Env, state string) {
	presenceMu.Lock()
	if time.Now().Before(presenceNext) {
		presenceMu.Unlock()
		return
	}
	send := presenceSend
	presenceMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		err := send(ctx, env, state)
		presenceMu.Lock()
		defer presenceMu.Unlock()
		if err != nil {
			if presenceGap < 60*time.Second {
				presenceGap *= 2
			}
			if presenceGap > 60*time.Second {
				presenceGap = 60 * time.Second
			}
			presenceNext = time.Now().Add(presenceGap)
			return
		}
		presenceGap = 5 * time.Second
		presenceNext = time.Now().Add(5 * time.Second)
	}()
}
