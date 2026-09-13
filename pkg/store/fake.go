package store

import (
	"context"
	"iter"
	"sort"
	"sync"

	"github.com/quantumwake/statefs.ai/pkg/event"
)

// Fake is the in-memory Store used by every consumer's tests. It models one
// tenant: displayName is unique, positions are contiguous, Find is
// containment. Concurrency-safe.
type Fake struct {
	mu     sync.Mutex
	byName map[string]string  // displayName -> id
	ns     map[string]*fakeNS // id -> namespace
	order  []string           // ids by creation, for Find ordering
	nextID int
}

type fakeNS struct {
	meta Namespace
	rows []event.Event
}

// NewFake returns an empty Fake.
func NewFake() *Fake {
	return &Fake{byName: map[string]string{}, ns: map[string]*fakeNS{}}
}

// Open implements Store.
func (f *Fake) Open(_ context.Context, displayName string, scope Scope) (Namespace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byName[displayName]; ok {
		return f.metaLocked(id), nil
	}

	f.nextID++
	id := "fake-" + pad(f.nextID)
	cp := Scope{}
	for k, v := range scope {
		cp[k] = v
	}

	f.ns[id] = &fakeNS{meta: Namespace{ID: id, DisplayName: displayName, Scope: cp}}
	f.byName[displayName] = id
	f.order = append(f.order, id)
	return f.metaLocked(id), nil
}

// Append implements Store.
func (f *Fake) Append(_ context.Context, ns string, events []event.Event, _ bool) (Position, error) {
	for _, e := range events {
		if err := e.Validate(); err != nil {
			return 0, joinErr(ErrInvalidEvent, err)
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.ns[ns]
	if !ok {
		return 0, ErrNotFound
	}

	start := Position(len(n.rows))
	n.rows = append(n.rows, events...)
	return start, nil
}

// Scan implements Store.
func (f *Fake) Scan(_ context.Context, ns string, from, to Position) iter.Seq2[event.Event, error] {
	return func(yield func(event.Event, error) bool) {
		f.mu.Lock()
		n, ok := f.ns[ns]
		if !ok {
			f.mu.Unlock()
			yield(event.Event{}, ErrNotFound)
			return
		}

		head := Position(len(n.rows))
		if to <= 0 || to > head {
			to = head
		}

		if from < 0 {
			from = 0
		}

		var snapshot []event.Event
		if from < to {
			snapshot = append(snapshot, n.rows[from:to]...)
		}

		f.mu.Unlock()
		for _, e := range snapshot {
			if !yield(e, nil) {
				return
			}
		}
	}
}

// Head implements Store.
func (f *Fake) Head(_ context.Context, ns string) (Position, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.ns[ns]
	if !ok {
		return 0, ErrNotFound
	}

	return Position(len(n.rows)), nil
}

// Find implements Store: containment, newest first.
func (f *Fake) Find(_ context.Context, filter Scope, limit int) ([]Namespace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := append([]string(nil), f.order...)
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	var out []Namespace
	for _, id := range ids {
		if !f.ns[id].meta.Scope.Contains(filter) {
			continue
		}

		out = append(out, f.metaLocked(id))
		if limit > 0 && len(out) >= limit {
			break
		}
	}

	return out, nil
}

func (f *Fake) metaLocked(id string) Namespace {
	m := f.ns[id].meta
	m.Head = Position(len(f.ns[id].rows))
	return m
}

func pad(n int) string {
	const digits = "0123456789"
	var b [6]byte
	for i := len(b) - 1; i >= 0; i-- {
		b[i] = digits[n%10]
		n /= 10
	}

	return string(b[:])
}

type joinedErr struct{ outer, inner error }

func (j joinedErr) Error() string   { return j.outer.Error() + ": " + j.inner.Error() }
func (j joinedErr) Unwrap() []error { return []error{j.outer, j.inner} }

func joinErr(outer, inner error) error { return joinedErr{outer, inner} }

// Describe implements Store.
func (f *Fake) Describe(_ context.Context, ns string, labels Scope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	n, ok := f.ns[ns]
	if !ok {
		return ErrNotFound
	}

	for k, v := range labels {
		n.meta.Scope[k] = v
	}

	return nil
}
