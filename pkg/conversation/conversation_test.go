package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

func ev(seq int64) event.Event {
	return event.Event{ID: event.NewID(), Seq: seq, TSMs: time.Now().UnixMilli(), SessionID: "s",
		Source: event.SourceClaudeCode, Kind: event.KindUserMessage, Role: event.RoleUser, Identity: "t",
		Content: json.RawMessage(`{"seq":` + itoa(seq) + `}`)}
}

func itoa(n int64) string { return string(rune('0' + n)) }

func TestWriterCoalescesAndDedupes(t *testing.T) {
	ctx := context.Background()
	st := store.NewFake()
	c, err := Open(ctx, st, "conv", store.Scope{"kind": "conversation"})
	if err != nil {
		t.Fatal(err)
	}

	w := NewWriter(c, 100)
	w.MaxEvents = 3
	w.MaxAge = time.Hour // only size triggers in this test
	var flushes int
	w.OnFlush = func(_ store.Position, batch []event.Event, err error) { flushes++ }

	a, b := ev(1), ev(2)
	for _, e := range []event.Event{a, b, a} { // a twice: dedupe
		if err := w.Add(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	if w.Pending() != 2 || flushes != 0 {
		t.Fatalf("want 2 pending, 0 flushes; got %d, %d", w.Pending(), flushes)
	}

	if err := w.Add(ctx, ev(3)); err != nil { // third distinct event: flush
		t.Fatal(err)
	}

	if flushes != 1 || w.Pending() != 0 {
		t.Fatalf("want 1 flush and empty buffer; got %d, %d", flushes, w.Pending())
	}

	head, _ := c.Head(ctx)
	if head != 3 {
		t.Fatalf("head want 3, got %d", head)
	}

	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if err := w.Add(ctx, ev(4)); !errors.Is(err, ErrClosed) {
		t.Fatalf("want ErrClosed, got %v", err)
	}
}

func TestWriterAgeFlush(t *testing.T) {
	ctx := context.Background()
	c, _ := Open(ctx, store.NewFake(), "conv", nil)
	w := NewWriter(c, 10)
	w.MaxAge = 20 * time.Millisecond
	done := make(chan struct{}, 1)
	w.OnFlush = func(store.Position, []event.Event, error) { done <- struct{}{} }
	_ = w.Add(ctx, ev(1))
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("age flush did not fire")
	}

	if head, _ := c.Head(ctx); head != 1 {
		t.Fatalf("head want 1, got %d", head)
	}
}

func TestSubscribeFollowsHead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st := store.NewFake()
	c, _ := Open(ctx, st, "conv", nil)
	_, _ = c.Append(ctx, false, ev(1), ev(2))

	got := make(chan int64, 8)
	go func() {
		for e, err := range c.Subscribe(ctx, 1, 10*time.Millisecond) {
			if err != nil {
				return
			}

			got <- e.Seq
		}
	}()

	if s := <-got; s != 2 {
		t.Fatalf("subscribe from 1 must start at seq 2, got %d", s)
	}

	_, _ = c.Append(ctx, false, ev(3))
	select {
	case s := <-got:
		if s != 3 {
			t.Fatalf("want seq 3, got %d", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("subscriber did not see the new row")
	}
}

func TestAttachAndScanRange(t *testing.T) {
	ctx := context.Background()
	st := store.NewFake()
	c, _ := Open(ctx, st, "conv", nil)
	_, _ = c.Append(ctx, false, ev(1), ev(2), ev(3))
	again := Attach(st, c.ID())
	var seqs []int64
	for e, err := range again.Scan(ctx, 1, 3) {
		if err != nil {
			t.Fatal(err)
		}

		seqs = append(seqs, e.Seq)
	}

	if len(seqs) != 2 || seqs[0] != 2 || seqs[1] != 3 {
		t.Fatalf("scan [1,3) got %v", seqs)
	}
}

// capped refuses like a member with caps: a batch over maxRows, or holding a
// row over maxRow bytes, is ErrTooLarge; failNext fails the next append.
type capped struct {
	store.Store
	maxRows, maxRow int
	failNext        error
	sent            [][]event.Event
}

func (c *capped) Append(ctx context.Context, ns string, events []event.Event, sync bool) (store.Position, error) {
	c.sent = append(c.sent, events)
	if err := c.failNext; err != nil {
		c.failNext = nil
		return 0, err
	}
	if len(events) > c.maxRows {
		return 0, store.ErrTooLarge
	}
	for _, e := range events {
		if len(e.Content) > c.maxRow {
			return 0, store.ErrTooLarge
		}
	}
	return c.Store.Append(ctx, ns, events, sync)
}

func contents(t *testing.T, c *Conversation) []string {
	var out []string
	for e, err := range c.Scan(context.Background(), 0, 0) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(e.Content))
	}
	return out
}

// A batch the member refuses as too large is split and lands in order; a
// row that alone is too large becomes a stub instead of being retried.
func TestWriterSplitsAndStubsWhatIsTooLarge(t *testing.T) {
	ctx := context.Background()
	st := &capped{Store: store.NewFake(), maxRows: 2, maxRow: 200}
	c, _ := Open(ctx, st, "conv", store.Scope{"kind": "conversation"})
	w := NewWriter(c, 100)
	w.MaxEvents, w.MaxAge = 100, time.Hour

	huge := ev(3)
	huge.Content = json.RawMessage(`{"text":"` + strings.Repeat("x", 500) + `"}`)
	for _, e := range []event.Event{ev(1), ev(2), huge, ev(4), ev(5)} {
		if err := w.Add(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}

	got := contents(t, c)
	if len(got) != 5 || got[0] != `{"seq":1}` || got[1] != `{"seq":2}` || got[3] != `{"seq":4}` || got[4] != `{"seq":5}` {
		t.Fatalf("order or count: %q", got)
	}
	if !json.Valid([]byte(got[2])) || !strings.Contains(got[2], `"dropped":true`) {
		t.Fatalf("the oversized row should be a stub: %s", got[2])
	}
	if w.Pending() != 0 {
		t.Fatalf("%d events left buffered", w.Pending())
	}
}

// When part of a split lands and a later part fails for another reason, only
// the part that did not land is kept for the retry, so nothing is written twice.
func TestWriterKeepsOnlyWhatDidNotLand(t *testing.T) {
	ctx := context.Background()
	st := &capped{Store: store.NewFake(), maxRows: 2, maxRow: 1 << 20}
	c, _ := Open(ctx, st, "conv", store.Scope{"kind": "conversation"})
	w := NewWriter(c, 100)
	w.MaxEvents, w.MaxAge = 100, time.Hour
	for i := int64(1); i <= 4; i++ {
		if err := w.Add(ctx, ev(i)); err != nil {
			t.Fatal(err)
		}
	}

	// 4 refused → [1,2] lands → [3,4] fails with a network error.
	boom := errors.New("network down")
	orig := st.Store
	st.Store = &failAfter{Store: orig, ok: 1, err: boom}
	if err := w.Flush(ctx); !errors.Is(err, boom) {
		t.Fatalf("flush: %v", err)
	}
	if w.Pending() != 2 {
		t.Fatalf("only the two that did not land should be kept, got %d", w.Pending())
	}

	st.Store = orig
	if err := w.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	if got := contents(t, c); len(got) != 4 || got[2] != `{"seq":3}` {
		t.Fatalf("after retry: %q", got)
	}
}

// failAfter lets ok appends through, then fails the rest with err.
type failAfter struct {
	store.Store
	ok  int
	err error
}

func (f *failAfter) Append(ctx context.Context, ns string, events []event.Event, sync bool) (store.Position, error) {
	if f.ok == 0 {
		return 0, f.err
	}
	f.ok--
	return f.Store.Append(ctx, ns, events, sync)
}
