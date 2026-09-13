package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/event"
	"github.com/quantumwake/statefs.ai/pkg/store"
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
