package plugin

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/quantumwake/parley/pkg/store"
)

// Find lists conversations on the server by scope labels (k=v pairs) and
// prints name and id; with heads set it also reads each head, in parallel
// (each head is a route, a ticket and a member read, about a second cold
// from a laptop, so it is opt-in). The directory search itself is one
// call: the scope is a JSONB column with a GIN index, so a tag filter is
// an index lookup, not a scan.
func Find(ctx context.Context, env Env, pairs []string, limit int, heads bool, w io.Writer) error {
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

	hs := make([]store.Position, len(metas))
	for i := range hs {
		hs[i] = store.HeadUnknown
	}

	if heads {
		Parallel(len(metas), 8, func(i int) {
			if h, err := st.Head(ctx, metas[i].ID); err == nil {
				hs[i] = h
			}
		})
	}

	for i, m := range metas {
		if heads {
			fmt.Fprintf(w, "%-58s %s %5d  %s\n", m.DisplayName, m.ID, hs[i], titleOf(m))
			continue
		}

		fmt.Fprintf(w, "%-58s %s  %s\n", m.DisplayName, m.ID, titleOf(m))
	}

	return nil
}

// Parallel runs fn(i) for i in [0,n) with at most width goroutines.
func Parallel(n, width int, fn func(i int)) {
	if width <= 0 {
		width = 1
	}

	sem := make(chan struct{}, width)
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		sem <- struct{}{}
		go func(i int) {
			defer func() { <-sem; done <- struct{}{} }()
			fn(i)
		}(i)
	}

	for i := 0; i < n; i++ {
		<-done
	}
}

// titleOf is the listing title: the title label, else the description.
func titleOf(m store.Namespace) string {
	if t := str(m.Scope["title"]); t != "" {
		return t
	}

	return str(m.Scope["description"])
}
