package plugin

import (
	"os"
	"strings"
	"testing"
	"time"
)

// A machine that has opted into nothing keeps nothing: no cache object at
// all, so the client behaves exactly as it did before any of this.
func TestNoCacheUntilTheMachineOptsIn(t *testing.T) {
	withConfig(t)
	t.Setenv("PARLEY_FEATURES", "")
	os.Unsetenv("PARLEY_FEATURES")

	env := EnvFromProcess()
	env.Directory = "https://directory.example"
	env.IdentityPath = "/keys/ana"
	env.DataDir = t.TempDir()

	if c := storeCache(env); c != nil {
		t.Fatal("a cache was built for a machine that asked for none")
	}
}

// The two switches are separate risks, so they are separate switches: a
// stale route is corrected by a 307, a stale ticket by a 401. Turning one
// on must not quietly turn the other on.
func TestEachSwitchKeepsOnlyItsOwn(t *testing.T) {
	withConfig(t)
	env := EnvFromProcess()
	env.Directory = "https://directory.example"
	env.IdentityPath = "/keys/ana"
	env.DataDir = t.TempDir()

	cases := []struct {
		features              string
		wantRoute, wantTicket bool
	}{
		{"route-cache", true, false},
		{"ticket-cache", false, true},
		{"route-cache,ticket-cache", true, true},
	}

	for _, tc := range cases {
		t.Setenv("PARLEY_FEATURES", tc.features)
		cache := storeCache(env)
		if cache == nil {
			t.Fatalf("%s: no cache was built", tc.features)
		}

		routeKey, ticketKey := "route\x00d\x00ns", "ticket\x00d\x00ns\x00read"
		cache.Put(routeKey, []byte("r"), time.Now().Add(time.Minute))
		cache.Put(ticketKey, []byte("t"), time.Now().Add(time.Minute))

		if _, ok := cache.Get(routeKey); ok != tc.wantRoute {
			t.Fatalf("%s: route kept = %v, want %v", tc.features, ok, tc.wantRoute)
		}

		if _, ok := cache.Get(ticketKey); ok != tc.wantTicket {
			t.Fatalf("%s: ticket kept = %v, want %v", tc.features, ok, tc.wantTicket)
		}
	}
}

// Anything that is neither is refused rather than kept blindly: a cache
// that keeps what nobody named is a cache nobody can reason about.
func TestAnUnknownKeyIsNotKept(t *testing.T) {
	withConfig(t)
	t.Setenv("PARLEY_FEATURES", "route-cache,ticket-cache")
	env := EnvFromProcess()
	env.Directory = "https://directory.example"
	env.IdentityPath = "/keys/ana"
	env.DataDir = t.TempDir()

	cache := storeCache(env)
	cache.Put("something\x00else", []byte("x"), time.Now().Add(time.Minute))
	if _, ok := cache.Get("something\x00else"); ok {
		t.Fatal("a key that is neither a route nor a ticket was kept")
	}
}

// The identity scopes the files, so two identities on one machine cannot
// read each other's tickets.
func TestTwoIdentitiesGetDifferentCacheDirectories(t *testing.T) {
	withConfig(t)
	t.Setenv("PARLEY_FEATURES", "ticket-cache")
	data := t.TempDir()

	seen := map[string]bool{}
	for _, identity := range []string{"/keys/ana", "/keys/bob"} {
		env := EnvFromProcess()
		env.Directory = "https://directory.example"
		env.IdentityPath = identity
		env.DataDir = data

		cache := storeCache(env)
		cache.Put("ticket\x00d\x00ns\x00read", []byte(identity), time.Now().Add(time.Minute))
		got, ok := cache.Get("ticket\x00d\x00ns\x00read")
		if !ok || string(got) != identity {
			t.Fatalf("%s read back %q", identity, got)
		}

		seen[identity] = true
	}

	// Walk what landed on disk: two identities, two directories.
	dirs, err := os.ReadDir(data + "/cache")
	if err != nil {
		t.Fatal(err)
	}

	if len(dirs) != 2 {
		names := make([]string, 0, len(dirs))
		for _, d := range dirs {
			names = append(names, d.Name())
		}

		t.Fatalf("cache directories = %d (%s), want one per identity", len(dirs), strings.Join(names, ", "))
	}
}
