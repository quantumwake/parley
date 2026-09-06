package store_test

import (
	"testing"

	"github.com/quantumwake/statefs.ai/pkg/store"
	"github.com/quantumwake/statefs.ai/pkg/store/storetest"
)

func TestFakeConformance(t *testing.T) {
	storetest.Conformance(t,
		func(t *testing.T) store.Store { return store.NewFake() },
		func(s string) string { return s })
}

func TestScopeContains(t *testing.T) {
	s := store.Scope{"kind": "conversation", "tags": []any{"a", "b"}, "n": 1}
	cases := []struct {
		f    store.Scope
		want bool
	}{
		{store.Scope{}, true},
		{store.Scope{"kind": "conversation"}, true},
		{store.Scope{"kind": "persona"}, false},
		{store.Scope{"tags": []string{"a"}}, true},
		{store.Scope{"tags": []string{"a", "c"}}, false},
		{store.Scope{"missing": "x"}, false},
		{store.Scope{"tags": "a"}, false},
	}
	for i, c := range cases {
		if got := s.Contains(c.f); got != c.want {
			t.Fatalf("case %d: Contains(%v) = %v, want %v", i, c.f, got, c.want)
		}
	}
}
