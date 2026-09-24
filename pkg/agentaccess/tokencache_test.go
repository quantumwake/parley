package agentaccess

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newProcess is what a second parley command is: a brand new Client with no
// memory of the first one, pointed at the same statefs.ai and the same
// identity, sharing only the cache directory.
func newProcess(c *Client, dir string) *Client {
	return &Client{Base: c.Base, Username: c.Username, Key: c.Key, Now: c.Now, UserAgent: c.UserAgent, CacheDir: dir}
}

func TestASecondProcessReusesTheCachedToken(t *testing.T) {
	f, c := setup(t)
	dir := t.TempDir()
	c.CacheDir = dir

	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	for range 5 {
		if _, err := newProcess(c, dir).People(context.Background(), "bo", 10); err != nil {
			t.Fatal(err)
		}
	}

	if n := f.signIns.Load(); n != 1 {
		t.Fatalf("six processes signed in %d times, want 1", n)
	}
}

func TestWithoutACacheDirEveryProcessSignsIn(t *testing.T) {
	f, c := setup(t)

	for range 3 {
		if _, err := newProcess(c, "").People(context.Background(), "bo", 10); err != nil {
			t.Fatal(err)
		}
	}

	if n := f.signIns.Load(); n != 3 {
		t.Fatalf("signed in %d times without a cache, want 3", n)
	}
}

func TestACachedTokenIsNotUsedPastItsExpiry(t *testing.T) {
	f, c := setup(t)
	dir := t.TempDir()
	c.CacheDir = dir

	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	// Later than the token's life: the cache file is still there, and must
	// not be presented.
	f.now = f.now.Add(16 * time.Minute)
	if _, err := newProcess(c, dir).People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	if n := f.signIns.Load(); n != 2 {
		t.Fatalf("signed in %d times across the expiry, want 2", n)
	}
}

func TestAnotherIdentitysCacheIsNotUsed(t *testing.T) {
	f, c := setup(t)
	dir := t.TempDir()
	c.CacheDir = dir

	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	// Same machine, same cache directory, a different agent: it must earn
	// its own token rather than read the one next to it.
	_, priv, _ := ed25519.GenerateKey(nil)
	other := &Client{Base: c.Base, Username: "bob-agent", Key: priv, Now: c.Now, CacheDir: dir}
	// The fake only signs in "ana-agent", so a refusal here is the proof
	// that bob did not walk off with ana's bearer.
	if _, err := other.People(context.Background(), "bo", 10); err == nil {
		t.Fatal("a second identity was served from the first one's cache")
	}

	if n := f.signIns.Load(); n != 1 {
		t.Fatalf("sign-ins %d, want 1 (ana's only)", n)
	}
}

func TestAnotherHostsCacheIsNotUsed(t *testing.T) {
	_, c := setup(t)
	dir := t.TempDir()
	c.CacheDir = dir

	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	// The same identity against a different statefs.ai. Nothing answers on
	// that base, so a reused token would show up as a people call that got
	// as far as the network; what must happen is a sign-in attempt there.
	elsewhere := &Client{Base: "http://127.0.0.1:1", Username: c.Username, Key: c.Key, Now: c.Now, CacheDir: dir}
	if tok, _ := elsewhere.readCache(); tok != "" {
		t.Fatal("a token earned at one statefs.ai was offered to another")
	}
}

func TestARefusedTokenIsDroppedFromTheCache(t *testing.T) {
	f, c := setup(t)
	dir := t.TempDir()
	c.CacheDir = dir

	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	// statefs.ai stops honouring the token it issued (a revoked key, a
	// restarted signer). The process that meets the 401 must clear the file,
	// or every later process presents the same dead token.
	f.revoke.Store(true)
	next := newProcess(c, dir)
	if _, err := next.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	if tok, _ := next.readCache(); tok == "t1" {
		t.Fatal("the refused token is still in the cache")
	}
}

func TestTheCacheFileIsPrivateAndAWidenedOneIsIgnored(t *testing.T) {
	f, c := setup(t)
	dir := t.TempDir()
	c.CacheDir = dir

	if _, err := c.People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	path := c.cachePath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("cache file is %v, want 0600", perm)
	}

	if d, err := os.Stat(filepath.Dir(path)); err != nil || d.Mode().Perm() != 0o700 {
		t.Fatalf("cache directory is %v (%v), want 0700", d.Mode().Perm(), err)
	}

	var v cached
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &v); err != nil || v.Token == "" {
		t.Fatalf("cache file does not hold a token: %v", err)
	}

	// Someone widens it. A token others can read is not reused.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := newProcess(c, dir).People(context.Background(), "bo", 10); err != nil {
		t.Fatal(err)
	}

	if n := f.signIns.Load(); n != 2 {
		t.Fatalf("signed in %d times after the file was widened, want 2", n)
	}

	if _, err := os.Stat(path); err == nil {
		// It was rewritten by the sign-in above; what matters is the mode.
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("the rewritten cache file is %v, want 0600", info.Mode().Perm())
		}
	}
}
