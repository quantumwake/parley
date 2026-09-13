package plugin

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/quantumwake/parley/pkg/store"
)

// labelKey is one scope key's tally: how many namespaces carry it, and how
// often each value appears.
type labelKey struct {
	namespaces int
	values     map[string]int
}

// freeText are scope keys whose values are prose or unique per namespace.
// Listing them would be noise, so Labels reports how many distinct values
// exist instead of naming them.
var freeText = map[string]bool{
	"title": true, "description": true, "name": true,
	"session": true, "started_ms": true, "task": true,
}

// maxValuesPerKey caps how many values are named for one key.
const maxValuesPerKey = 12

// Labels reports the scope keys in use and, for the enumerable ones, their
// values with counts. It is the missing first step of a search: an agent
// cannot narrow by label unless it knows which labels exist.
//
// This is a client-side approximation and should not stay one. Counting the
// keys means paging every namespace's metadata, which is O(rows) transferred
// for an O(keys) answer, and it is capped, so past the cap the answer is a
// sample of the catalogue rather than the catalogue. The aggregate belongs
// in the directory, next to the scope search it summarises: one grouped
// query over JSONB, scoped by the caller's own grants. Recorded as delta 11
// in docs/handoffs/HANDOFF-2026-09-05-statefs-core-requests.md; this
// function goes away when that lands.
//
// It sees exactly what the caller may see, because it aggregates the same
// directory listing the caller could read itself.
func Labels(ctx context.Context, env Env, limit int, w io.Writer) error {
	st, err := StoreFromEnv(env)
	if err != nil {
		return err
	}

	if limit <= 0 {
		limit = 500
	}

	metas, err := st.Find(ctx, store.Scope{"kind": "conversation"}, limit)
	if err != nil {
		return err
	}

	if len(metas) == 0 {
		return fmt.Errorf("no conversations are visible to this identity yet")
	}

	keys := map[string]*labelKey{}
	note := func(k, v string) {
		e := keys[k]
		if e == nil {
			e = &labelKey{values: map[string]int{}}
			keys[k] = e
		}

		e.namespaces++
		if v != "" && !freeText[k] {
			e.values[v]++
		}
	}

	for _, m := range metas {
		for k, raw := range m.Scope {
			switch v := raw.(type) {
			case []any: // tags
				note(k, "")
				for _, t := range v {
					if s, ok := t.(string); ok {
						keys[k].values[s]++
					}
				}
			case string:
				note(k, v)
			default:
				note(k, fmt.Sprint(v))
			}
		}
	}

	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}

	sort.Slice(names, func(i, j int) bool {
		if keys[names[i]].namespaces != keys[names[j]].namespaces {
			return keys[names[i]].namespaces > keys[names[j]].namespaces
		}

		return names[i] < names[j]
	})

	fmt.Fprintf(w, "labels across %d conversations visible to this identity:\n\n", len(metas))
	defer func() {
		if len(metas) >= limit {
			fmt.Fprintf(w, "\nNOTE: this is a sample. %d conversations were read, which is the cap,\nso keys or values beyond it are missing. Raise --limit, or narrow first.\n", limit)
		}
	}()
	for _, k := range names {
		e := keys[k]
		if freeText[k] {
			fmt.Fprintf(w, "%-14s %4d conversations   (free text, not worth filtering on)\n", k, e.namespaces)
			continue
		}

		fmt.Fprintf(w, "%-14s %4d conversations   %s\n", k, e.namespaces, topValues(e.values))
	}

	fmt.Fprintf(w, "\nfilter with these, e.g. `parley find %s` or the search tool's tag and query.\n", exampleFilter(keys))
	return nil
}

// topValues renders the most common values for one key, most frequent first.
func topValues(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}

	vals := make([]string, 0, len(counts))
	for v := range counts {
		vals = append(vals, v)
	}

	sort.Slice(vals, func(i, j int) bool {
		if counts[vals[i]] != counts[vals[j]] {
			return counts[vals[i]] > counts[vals[j]]
		}

		return vals[i] < vals[j]
	})

	shown := vals
	extra := 0
	if len(shown) > maxValuesPerKey {
		extra = len(shown) - maxValuesPerKey
		shown = shown[:maxValuesPerKey]
	}

	var b strings.Builder
	for i, v := range shown {
		if i > 0 {
			b.WriteString("  ")
		}

		fmt.Fprintf(&b, "%s(%d)", v, counts[v])
	}

	if extra > 0 {
		fmt.Fprintf(&b, "  +%d more", extra)
	}

	return b.String()
}

// exampleFilter picks a real key and value from what was found, so the hint
// is something the caller can actually run.
func exampleFilter(keys map[string]*labelKey) string {
	for _, k := range []string{"tags", "agent", "date", "persona", "mode"} {
		e := keys[k]
		if e == nil || len(e.values) == 0 {
			continue
		}

		best, n := "", 0
		for v, c := range e.values {
			if c > n || (c == n && v < best) {
				best, n = v, c
			}
		}

		return k + "=" + best
	}

	return "kind=conversation"
}
