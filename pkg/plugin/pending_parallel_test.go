package plugin

import (
	"bytes"
	"context"
	"iter"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

// delayingStore sleeps at the start of every Scan, then reads through.
// That is one round trip per conversation.
type delayingStore struct {
	store.Store
	delay time.Duration
}

func (d delayingStore) Scan(ctx context.Context, ns string, from, to store.Position) iter.Seq2[event.Event, error] {
	timer := time.NewTimer(d.delay)
	select {
	case <-ctx.Done():
		timer.Stop()
		return func(yield func(event.Event, error) bool) { yield(event.Event{}, ctx.Err()) }
	case <-timer.C:
	}
	return d.Store.Scan(ctx, ns, from, to)
}

func TestPendingReadsConversationsTogether(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	names := []string{"alpha", "bravo", "charlie", "delta"}
	follow(t, a, names...)
	follow(t, b, names...)
	ctx := context.Background()
	for _, name := range names {
		if err := Post(ctx, b, name, "comment", "row-"+name, "", "", nil, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
	}

	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}
	const delay = 200 * time.Millisecond
	start := time.Now()
	items, ran, failed := pendingRound(ctx, a, delayingStore{Store: st, delay: delay}, Subscriptions(a))
	took := time.Since(start)
	if !ran {
		t.Fatal("the round did not run")
	}
	if len(failed) != 0 {
		t.Fatal(failed)
	}
	// Four sequential scans would be 800ms. Four at once is one delay,
	// plus a little scheduling. A loop that waits for each scan fails this.
	if took >= time.Duration(len(names))*delay {
		t.Fatalf("reads stacked: %s for %d conversations", took, len(names))
	}

	got := make([]string, len(items))
	for i, it := range items {
		got[i] = it.sub.Name
	}
	want := make([]string, 0, len(names))
	for _, sub := range Subscriptions(a) {
		want = append(want, sub.Name)
	}
	if len(got) != len(want) {
		t.Fatalf("delivered %v, subscriptions %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order changed: got %v want %v", got, want)
		}
	}
}

func TestPollerScansConversationsTogether(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	names := []string{"alpha", "bravo", "charlie", "delta"}
	follow(t, a, names...)
	st, err := StoreFromEnv(a)
	if err != nil {
		t.Fatal(err)
	}

	groups := map[string]*nsGroup{}
	for _, sub := range Subscriptions(a) {
		groups[sub.ID] = &nsGroup{id: sub.ID, name: sub.Name, from: 0}
	}

	const delay = 200 * time.Millisecond
	start := time.Now()
	scans, scanErr, _ := scanGroups(context.Background(), context.Background(), a, delayingStore{Store: st, delay: delay}, groups)
	took := time.Since(start)
	if len(scanErr) != 0 {
		t.Fatal(scanErr)
	}
	if len(scans) != len(names) {
		t.Fatalf("scanned %d of %d", len(scans), len(names))
	}
	// Four sequential scans would be 800ms. Four at once is one delay.
	// A loop that waits for each scan fails this.
	if took >= time.Duration(len(names))*delay {
		t.Fatalf("scans stacked: %s for %d conversations", took, len(names))
	}
}
