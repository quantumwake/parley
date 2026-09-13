// Package storetest holds the contract every Store implementation must
// pass. Run it against the Fake in unit tests and against the statefs
// adapter in the integration test (dev.statefs.ai).
package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/quantumwake/statefs.ai/pkg/event"
	"github.com/quantumwake/statefs.ai/pkg/store"
)

// Conformance runs the contract. open returns a fresh Store; name makes
// display names unique per run so a shared backend does not collide.
// Every namespace it opens carries purpose:conformance in its scope, and
// Created collects them so an integration test can delete them after.
func Conformance(t *testing.T, open func(t *testing.T) store.Store, name func(string) string) {
	ConformanceTracked(t, open, name, nil)
}

// ConformanceTracked is Conformance with a callback for every namespace
// the suite creates (id and display name), for cleanup on real backends.
func ConformanceTracked(t *testing.T, open func(t *testing.T) store.Store, name func(string) string, created func(store.Namespace)) {
	t.Helper()
	ctx := context.Background()
	track := func(ns store.Namespace, err error) (store.Namespace, error) {
		if err == nil && created != nil {
			created(ns)
		}

		return ns, err
	}
	tag := func(s store.Scope) store.Scope {
		out := store.Scope{"purpose": "conformance"}
		for k, v := range s {
			out[k] = v
		}

		return out
	}

	t.Run("open is idempotent on display name", func(t *testing.T) {
		s := open(t)
		a, err := track(s.Open(ctx, name("conv-a"), tag(store.Scope{"kind": "conversation", "tags": []string{"x"}})))
		if err != nil {
			t.Fatal(err)
		}

		b, err := track(s.Open(ctx, name("conv-a"), tag(store.Scope{"kind": "other"})))
		if err != nil {
			t.Fatal(err)
		}

		if a.ID != b.ID {
			t.Fatalf("second Open must return the same namespace: %s vs %s", a.ID, b.ID)
		}

		if b.Scope["kind"] != "conversation" {
			t.Fatalf("Open must not rewrite an existing scope: %v", b.Scope)
		}
	})

	t.Run("positions are contiguous and readable after append", func(t *testing.T) {
		s := open(t)
		ns, err := track(s.Open(ctx, name("conv-b"), tag(store.Scope{"kind": "conversation"})))
		if err != nil {
			t.Fatal(err)
		}

		p0, err := s.Append(ctx, ns.ID, batch(3, 1), false)
		if err != nil {
			t.Fatal(err)
		}

		if p0 != 0 {
			t.Fatalf("first append must start at 0, got %d", p0)
		}

		p1, err := s.Append(ctx, ns.ID, batch(2, 4), false)
		if err != nil {
			t.Fatal(err)
		}

		if p1 != 3 {
			t.Fatalf("second append must start at 3, got %d", p1)
		}

		head, err := s.Head(ctx, ns.ID)
		if err != nil || head != 5 {
			t.Fatalf("head want 5, got %d (%v)", head, err)
		}

		var seqs []int64
		for e, err := range s.Scan(ctx, ns.ID, 0, 0) {
			if err != nil {
				t.Fatal(err)
			}

			seqs = append(seqs, e.Seq)
		}

		if len(seqs) != 5 || seqs[0] != 1 || seqs[4] != 5 {
			t.Fatalf("scan must return all rows in order, got %v", seqs)
		}

		var mid []int64
		for e, err := range s.Scan(ctx, ns.ID, 1, 3) {
			if err != nil {
				t.Fatal(err)
			}

			mid = append(mid, e.Seq)
		}

		if len(mid) != 2 || mid[0] != 2 || mid[1] != 3 {
			t.Fatalf("scan [1,3) want seq 2,3 got %v", mid)
		}
	})

	t.Run("invalid events are refused before any write", func(t *testing.T) {
		s := open(t)
		ns, _ := track(s.Open(ctx, name("conv-c"), tag(store.Scope{"kind": "conversation"})))
		bad := batch(1, 1)
		bad[0].Kind = "nope"
		if _, err := s.Append(ctx, ns.ID, bad, false); !errors.Is(err, store.ErrInvalidEvent) {
			t.Fatalf("want ErrInvalidEvent, got %v", err)
		}

		if head, _ := s.Head(ctx, ns.ID); head != 0 {
			t.Fatalf("nothing may be written on validation failure, head=%d", head)
		}
	})

	t.Run("describe merges labels", func(t *testing.T) {
		s := open(t)
		ns, _ := track(s.Open(ctx, name("conv-d"), tag(store.Scope{"kind": "conversation", "tags": []string{"a"}})))
		if err := s.Describe(ctx, ns.ID, store.Scope{"title": "first prompt", "description": "what it is about"}); err != nil {
			t.Fatal(err)
		}

		got, err := s.Find(ctx, store.Scope{"title": "first prompt"}, 5)
		if err != nil || len(got) != 1 || got[0].Scope["kind"] != "conversation" || got[0].Scope["description"] != "what it is about" {
			t.Fatalf("describe must merge, keeping existing keys: %v %v", got, err)
		}
	})

	t.Run("unknown namespace is ErrNotFound", func(t *testing.T) {
		s := open(t)
		if _, err := s.Head(ctx, "does-not-exist"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("find is containment newest first", func(t *testing.T) {
		s := open(t)
		run := name("run") // a per-run label so a shared backend does not leak earlier rows in
		_, _ = track(s.Open(ctx, name("f1"), tag(store.Scope{"kind": "conversation", "run": run, "agent": "a1", "tags": []string{"x", "y"}})))
		_, _ = track(s.Open(ctx, name("f2"), tag(store.Scope{"kind": "conversation", "run": run, "agent": "a2", "tags": []string{"x"}})))
		_, _ = track(s.Open(ctx, name("f3"), tag(store.Scope{"kind": "persona", "run": run})))
		got, err := s.Find(ctx, store.Scope{"kind": "conversation", "run": run, "tags": []string{"x"}}, 10)
		if err != nil {
			t.Fatal(err)
		}

		if len(got) != 2 {
			t.Fatalf("want 2 conversations tagged x, got %d", len(got))
		}

		if got[0].DisplayName != name("f2") {
			t.Fatalf("newest first: want %s got %s", name("f2"), got[0].DisplayName)
		}

		one, _ := s.Find(ctx, store.Scope{"run": run, "agent": "a1"}, 10)
		if len(one) != 1 || one[0].DisplayName != name("f1") {
			t.Fatalf("scalar containment failed: %v", one)
		}
	})
}

func batch(n int, firstSeq int64) []event.Event {
	out := make([]event.Event, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, event.Event{
			ID: event.NewID(), Seq: firstSeq + int64(i), TSMs: time.Now().UnixMilli(),
			SessionID: "s", Source: event.SourceClaudeCode, Kind: event.KindUserMessage,
			Role: event.RoleUser, Identity: "t", Content: json.RawMessage(`{"i":` + itoa(i) + `}`),
		})
	}

	return out
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}

	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}

	return string(b)
}
