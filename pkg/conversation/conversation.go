// Package conversation is the product's verb set over one namespace: open
// it by name, append completed blocks with coalescing and dedupe, scan a
// range, subscribe from a cursor. It is the only package the plugin, the
// console, the job runner and a future gateway shell call; all of them
// hand it a store.Store (the statefs adapter in production, the Fake in
// tests).
package conversation

import (
	"context"
	"errors"
	"iter"
	"sync"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/event"
	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Conversation is one open namespace.
type Conversation struct {
	st store.Store
	ns store.Namespace
}

// Open returns the conversation named displayName, creating it with scope
// when absent (idempotent on the name, per the store contract).
func Open(ctx context.Context, st store.Store, displayName string, scope store.Scope) (*Conversation, error) {
	ns, err := st.Open(ctx, displayName, scope)
	if err != nil {
		return nil, err
	}

	return &Conversation{st: st, ns: ns}, nil
}

// Attach wraps an already-known namespace id without a directory call.
func Attach(st store.Store, id string) *Conversation {
	return &Conversation{st: st, ns: store.Namespace{ID: id, Head: store.HeadUnknown}}
}

// Namespace is the directory record this conversation was opened with.
func (c *Conversation) Namespace() store.Namespace { return c.ns }

// ID is the statefs namespace handle.
func (c *Conversation) ID() string { return c.ns.ID }

// Append writes events now, in order, and returns the first position.
func (c *Conversation) Append(ctx context.Context, sync bool, events ...event.Event) (store.Position, error) {
	return c.st.Append(ctx, c.ns.ID, events, sync)
}

// Head is the next append position.
func (c *Conversation) Head(ctx context.Context) (store.Position, error) {
	return c.st.Head(ctx, c.ns.ID)
}

// Scan yields [from, to) in order; to <= 0 pins the head at call time.
func (c *Conversation) Scan(ctx context.Context, from, to store.Position) iter.Seq2[event.Event, error] {
	return c.st.Scan(ctx, c.ns.ID, from, to)
}

// Subscribe yields every event from position `from` onward and then keeps
// following the head until ctx is done. Today it polls at interval (the
// statefs feed consumer mode replaces the poll behind this same call
// later). The caller owns the cursor: persist the position of the last
// event it acted on and pass it back as `from` next time.
func (c *Conversation) Subscribe(ctx context.Context, from store.Position, interval time.Duration) iter.Seq2[event.Event, error] {
	if interval <= 0 {
		interval = time.Second
	}

	return func(yield func(event.Event, error) bool) {
		cur := from
		for {
			head, err := c.st.Head(ctx, c.ns.ID)
			if err != nil {
				yield(event.Event{}, err)
				return
			}

			for e, err := range c.st.Scan(ctx, c.ns.ID, cur, head) {
				if !yield(e, err) {
					return
				}

				if err == nil {
					cur++
				}
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(interval):
			}
		}
	}
}

// Writer coalesces appends: events accumulate until MaxEvents, MaxBytes or
// MaxAge, then land as one Append. Duplicate event ids inside the writer's
// memory window are dropped before they reach the store, which is how a
// plugin retrying its spool avoids double rows today (the upstream batch-id
// seam makes this exact later). One Writer per conversation per process.
type Writer struct {
	conv      *Conversation
	MaxEvents int           // flush at this many buffered events (default 100)
	MaxBytes  int           // flush when buffered content exceeds this (default 64 KiB)
	MaxAge    time.Duration // flush this long after the first buffered event (default 250 ms)
	Sync      bool          // request replica-confirmed durability on every flush
	OnFlush   func(first store.Position, events []event.Event, err error) // optional observer

	mu      sync.Mutex
	buf     []event.Event
	bytes   int
	timer   *time.Timer
	seen    map[string]struct{}
	seenQ   []string
	seenMax int
	closed  bool
	flushMu sync.Mutex // serializes flushes so positions stay in order
}

// NewWriter builds a Writer with the defaults above; seenWindow bounds the
// dedupe memory (default 10,000 ids).
func NewWriter(conv *Conversation, seenWindow int) *Writer {
	if seenWindow <= 0 {
		seenWindow = 10_000
	}

	return &Writer{conv: conv, MaxEvents: 100, MaxBytes: 64 << 10, MaxAge: 250 * time.Millisecond, seen: map[string]struct{}{}, seenMax: seenWindow}
}

// ErrClosed is returned by Add after Close.
var ErrClosed = errors.New("conversation: writer closed")

// Add buffers one event. It returns quickly; the store is touched on the
// next flush. Invalid events are refused here, never buffered.
func (w *Writer) Add(ctx context.Context, e event.Event) error {
	if err := e.Validate(); err != nil {
		return err
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return ErrClosed
	}

	if _, dup := w.seen[e.ID]; dup {
		w.mu.Unlock()
		return nil
	}

	w.remember(e.ID)
	w.buf = append(w.buf, e)
	w.bytes += len(e.Content)
	full := len(w.buf) >= w.MaxEvents || w.bytes >= w.MaxBytes
	if w.timer == nil && !full {
		w.timer = time.AfterFunc(w.MaxAge, func() { _ = w.Flush(context.Background()) })
	}

	w.mu.Unlock()
	if full {
		return w.Flush(ctx)
	}

	return nil
}

// Flush appends everything buffered as one batch.
func (w *Writer) Flush(ctx context.Context) error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()
	w.mu.Lock()
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}

	batch := w.buf
	w.buf = nil
	w.bytes = 0
	w.mu.Unlock()
	if len(batch) == 0 {
		return nil
	}

	first, err := w.conv.Append(ctx, w.Sync, batch...)
	if w.OnFlush != nil {
		w.OnFlush(first, batch, err)
	}

	if err != nil {
		// Put the batch back at the front so a retry preserves order; the
		// ids stay remembered, so a caller re-adding them is a no-op.
		w.mu.Lock()
		w.buf = append(batch, w.buf...)
		for _, e := range batch {
			w.bytes += len(e.Content)
		}

		w.mu.Unlock()
		return err
	}

	return nil
}

// Close flushes and refuses further Adds.
func (w *Writer) Close(ctx context.Context) error {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	return w.Flush(ctx)
}

// Pending reports how many events are buffered.
func (w *Writer) Pending() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.buf)
}

func (w *Writer) remember(id string) {
	w.seen[id] = struct{}{}
	w.seenQ = append(w.seenQ, id)
	if len(w.seenQ) > w.seenMax {
		old := w.seenQ[0]
		w.seenQ = w.seenQ[1:]
		delete(w.seen, old)
	}
}
