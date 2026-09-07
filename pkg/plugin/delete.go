package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"

	"strings"

	"github.com/quantumwake/statefs.ai/pkg/store"
	adapter "github.com/quantumwake/statefs.ai/pkg/store/statefs"
)

type storeIface = store.Store

// IsRefusedText recognizes the directory's capability refusal.
func IsRefusedText(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), "manage capability"))
}

// DeleteConversation removes a conversation everywhere (directory pin and
// every member's copy). Needs a credential with the manage capability on
// this identity; the everyday plugin key is read and write only, so pass
// the manage identity file with --identity or STATEFS_KEY_FILE.
func DeleteConversation(ctx context.Context, env Env, name string, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	a, ok := st.(*adapter.Store)
	if !ok {
		return errors.New("delete: only meaningful against statefs.io")
	}

	id, err := resolveAny(ctx, env, st, name)
	if err != nil {
		return err
	}

	if _, err := a.Client().DeleteNamespace(ctx, id); err != nil {
		if IsRefusedText(err) {
			return fmt.Errorf("delete refused: this credential lacks the manage capability; use a manage-capable identity (parley delete %s --identity ~/.statefs-ai/identity-manage)", name)
		}

		return err
	}

	forgetName(env, name)
	_ = Leave(env, name, io.Discard)
	fmt.Fprintf(w, "deleted %s (%s)\n", name, id)
	return nil
}

// resolveAny resolves a shared conversation or a recorded session by name or id.
func resolveAny(ctx context.Context, env Env, st storeIface, name string) (string, error) {
	if looksLikeID(name) {
		return name, nil
	}

	if id := namesGet(env, name); id != "" {
		return id, nil
	}

	id, err := LookupName(ctx, env, st, name)
	if err != nil {
		return "", err
	}

	if id == "" {
		return "", fmt.Errorf("no conversation named %q", name)
	}

	return id, nil
}
