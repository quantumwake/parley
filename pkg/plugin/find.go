package plugin

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Find lists conversations on the server by scope labels (k=v pairs) and
// prints name, id and head. It is the source of truth the load test and
// people use; the local names record is only a cache.
func Find(ctx context.Context, env Env, pairs []string, limit int, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	filter := store.Scope{"kind": "conversation"}
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok {
			return fmt.Errorf("find: %q is not k=v", p)
		}

		filter[k] = v
	}

	metas, err := st.Find(ctx, filter, limit)
	if err != nil {
		return err
	}

	for _, m := range metas {
		head := m.Head
		if head == store.HeadUnknown {
			if h, err := st.Head(ctx, m.ID); err == nil {
				head = h
			}
		}

		fmt.Fprintf(w, "%-48s %s %d\n", m.DisplayName, m.ID, head)
	}

	return nil
}
