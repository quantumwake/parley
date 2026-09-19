// Package store is the S2 seam: the one port through which the product
// reads and writes conversations. The statefs adapter is the only
// production implementation; Fake is the test double every consumer
// builds against; storetest.Conformance is the contract both must pass.
package store

import (
	"context"
	"errors"
	"iter"

	"github.com/quantumwake/parley/pkg/event"
)

// Scope is the directory's searchable JSONB on a namespace. The "tags" key
// is reserved for a string array (statefs convention); "kind" is the
// product's namespace kind (conversation, persona, agent).
type Scope map[string]any

// Position is a statefs row position: permanent, contiguous from 0.
type Position int64

// Namespace is a conversation, persona or agent as the directory knows it.
type Namespace struct {
	ID          string   // statefs namespace handle (UUID); the only stable key
	DisplayName string   // human name, unique per tenant on statefs.io
	Scope       Scope    // search labels
	Owner       string   // owning membership id; "" = tenant-wide (or unknown on a fake)
	Head        Position // rows so far; HeadUnknown when the caller did not ask
}

// HeadUnknown marks a Namespace whose Head was not fetched.
const HeadUnknown Position = -1

// Store is the port. Semantics are fixed by storetest.Conformance:
// positions are contiguous from 0; Scan after Append sees the rows
// (read-your-writes against the primary); Open is idempotent on
// displayName; Find is scope containment, not equality; errors are typed.
type Store interface {
	// Open returns the namespace named displayName, creating it with scope
	// when absent. The scope of an existing namespace is not rewritten.
	Open(ctx context.Context, displayName string, scope Scope) (Namespace, error)

	// Append writes events at the tail, in order, and returns the position
	// of the first one. sync requests replica-confirmed durability.
	// Delivery is at-least-once: a retry after a timeout may duplicate rows,
	// so callers keep event_id and readers dedupe until the batch-id seam
	// lands upstream (handoff delta 2).
	Append(ctx context.Context, ns string, events []event.Event, sync bool) (Position, error)

	// Scan yields events in [from, to) in position order. to <= 0 means the
	// head at call time, so a scan under live writes terminates.
	Scan(ctx context.Context, ns string, from, to Position) iter.Seq2[event.Event, error]

	// Head is the number of rows in ns (the next append position).
	Head(ctx context.Context, ns string) (Position, error)

	// Find lists namespaces whose scope contains filter, newest first.
	Find(ctx context.Context, filter Scope, limit int) ([]Namespace, error)

	// Describe merges labels into a namespace's scope (existing keys not
	// named survive). Used for title, description and tags after birth.
	Describe(ctx context.Context, ns string, labels Scope) error
}

// Typed errors so callers branch without string matching.
var (
	ErrNotFound = errors.New("store: namespace not found")
	ErrRefused  = errors.New("store: refused (no grant, cordoned, or leaderless)")
	// ErrUnauthenticated is a refusal of the credential itself (HTTP 401)
	// rather than of access: rejected, or a token that expired. It is also
	// ErrRefused, so callers that only ask "refused?" are unchanged.
	ErrUnauthenticated        error = unauthenticated{}
	ErrDurabilityNotConfirmed       = errors.New("store: rows are leader-durable but the quorum did not confirm in time")
	ErrInvalidEvent                 = errors.New("store: invalid event")
	// ErrTooLarge is a definite refusal of a request's size (HTTP 413, or
	// 507 when the member can never serve it): nothing was written, and the
	// same bytes will be refused again, so callers split or shrink instead
	// of retrying unchanged.
	ErrTooLarge = errors.New("store: refused as too large")
)

// Contains reports whether scope has every key of filter with an equal
// value; string arrays match element-wise (the directory's @> semantics).
func (s Scope) Contains(filter Scope) bool {
	for k, want := range filter {
		got, ok := s[k]
		if !ok {
			return false
		}

		if !valueContains(got, want) {
			return false
		}
	}

	return true
}

func valueContains(got, want any) bool {
	wa, wantIsList := toStrings(want)
	if !wantIsList {
		return got == want
	}

	ga, gotIsList := toStrings(got)
	if !gotIsList {
		return false
	}

	for _, w := range wa {
		found := false
		for _, g := range ga {
			if g == w {
				found = true
				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

func toStrings(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}

			out = append(out, s)
		}

		return out, true
	}

	return nil, false
}

type unauthenticated struct{}

func (unauthenticated) Error() string {
	return "store: not authenticated (the credential was rejected, or its token expired)"
}

func (unauthenticated) Is(target error) bool { return target == ErrRefused }
