package client

// cache.go — somewhere for a client to keep what outlives it.
//
// Every statefs CLI is a short-lived process: it resolves a namespace,
// mints a ticket, does one thing and exits. The next command repeats all
// three, and each is a fresh DNS+TCP+TLS handshake to the directory.
// Measured from a laptop against production on 2026-09-25: 168 ms round
// trip, 190 ms more for TLS, ~0.5 s before a byte comes back — and a
// `parley post` at 1.5 s, most of it that. The route and the ticket are
// the two answers a process throws away on exit and asks for again.
//
// So a Client may be given somewhere to put them. It is OFF by default,
// because a library that writes to a user's disk unasked is rude, and the
// contract is the one http.Client has with its Transport: nil means the
// behaviour you already had, byte for byte.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Cache is where a Client keeps answers that outlive the process. An
// implementation must be safe for concurrent use by several processes, not
// only several goroutines: on one laptop, several agent seats run at once.
//
// A Cache MUST be scoped to ONE identity. Two identities that shared one
// would hand each other tickets, which are also MAC keys — see NewDiskCache,
// which takes the identity so the only implementation here cannot get it
// wrong.
type Cache interface {
	// Get answers the value and whether it is present and still live.
	Get(key string) ([]byte, bool)
	// Put stores a value until expires. A zero expiry means "until dropped".
	Put(key string, value []byte, expires time.Time)
	// Drop forgets one key. Dropping what is not there is not an error.
	Drop(key string)
}

// DiskCache keeps a client's answers under one directory, one file per
// key, 0600 inside a 0700 directory that it owns. It is safe across processes: a write goes to a
// unique temporary file and is renamed, so a reader sees a whole file or
// none, and two writers cannot interleave.
type DiskCache struct{ dir string }

// NewDiskCache builds a cache under dir for ONE identity, whose name is
// part of the path — so two identities on one machine cannot see each
// other's tickets even if a caller passes the same dir for both. An empty
// identity is refused with a nil cache rather than quietly sharing.
func NewDiskCache(dir, identity string) *DiskCache {
	if dir == "" || identity == "" {
		return nil
	}

	// The identity is hashed rather than spelled: it can be an email or a
	// long machine name, and a cache path is not the place to leak either.
	sum := sha256.Sum256([]byte(identity))
	return &DiskCache{dir: filepath.Join(dir, hex.EncodeToString(sum[:8]))}
}

// entry is what a cache file holds.
type entry struct {
	Value   []byte    `json:"value"`
	Expires time.Time `json:"expires,omitempty"`
}

// usable answers whether d.dir is a directory we may keep secrets in: a
// real directory, ours, and private.
//
// Checking the file's mode is not enough. MkdirAll does nothing to a
// directory that already exists, so a cache directory left at 0777 by an
// old umask, another tool or a careless mkdir -p stays 0777 while every
// file inside is a blameless 0600 - and anyone who can write that
// directory can rename their own file over the route entry. The next
// command then talks to a member of their choosing and hands it the
// ticket, which is the MAC key.
//
// It is never repaired, only refused. A directory we did not create at
// the mode we wanted is a directory we do not understand, and chmod-ing
// someone else's directory is not a library's business.
func (d *DiskCache) usable() bool {
	info, err := os.Lstat(d.dir)
	if err != nil {
		return false
	}

	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}

	if info.Mode().Perm()&0o077 != 0 {
		return false
	}

	return dirOwnedByUs(info)
}

// dirOwnedByUs is the ownership check. A test cannot make a directory
// owned by somebody else without being root, so the comparison itself is
// exercised only in the field; replacing this is how a test reaches the
// refusal path that depends on it.
var dirOwnedByUs = ownedByUs

func (d *DiskCache) path(key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(d.dir, hex.EncodeToString(sum[:])+".json")
}

// Get answers a live value. A file that is expired, unreadable, or whose
// mode has been widened is treated as absent — and a widened one is
// removed, because a ticket others can read is a ticket to replace.
func (d *DiskCache) Get(key string) ([]byte, bool) {
	if d == nil {
		return nil, false
	}

	if !d.usable() {
		return nil, false
	}

	path := d.path(key)
	// Lstat, not Stat: Stat resolves a symlink, so a link planted where a
	// cache file should be would be read as whatever it points at.
	info, err := os.Lstat(path)
	if err != nil {
		return nil, false
	}

	if !info.Mode().IsRegular() {
		return nil, false
	}

	if info.Mode().Perm()&0o077 != 0 {
		_ = os.Remove(path)
		return nil, false
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}

	var e entry
	if err := json.Unmarshal(b, &e); err != nil {
		return nil, false
	}

	if !e.Expires.IsZero() && !time.Now().Before(e.Expires) {
		_ = os.Remove(path)
		return nil, false
	}

	return e.Value, true
}

// Put stores a value. Failure is silent: a cache is a saving, never a
// requirement, and a process that cannot write one simply asks again.
func (d *DiskCache) Put(key string, value []byte, expires time.Time) {
	if d == nil {
		return
	}

	if err := os.MkdirAll(d.dir, 0o700); err != nil {
		return
	}

	// MkdirAll is satisfied by a directory that already exists at any
	// mode, so the check has to come after it, not instead of it.
	if !d.usable() {
		return
	}

	b, err := json.Marshal(entry{Value: value, Expires: expires})
	if err != nil {
		return
	}

	f, err := os.CreateTemp(d.dir, ".put-*")
	if err != nil {
		return
	}

	tmp := f.Name()
	defer os.Remove(tmp) // a no-op once the rename below has moved it

	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return
	}

	if _, err := f.Write(b); err != nil {
		f.Close()
		return
	}

	if err := f.Close(); err != nil {
		return
	}

	_ = os.Rename(tmp, d.path(key))
}

// Drop forgets one key.
func (d *DiskCache) Drop(key string) {
	if d == nil {
		return
	}

	_ = os.Remove(d.path(key))
}

// cacheGet and cachePut are the Client's side: they do nothing at all
// when no cache was given, which is what keeps nil byte-identical.
func (c *Client) cacheGet(key string) ([]byte, bool) {
	if c.Cache == nil {
		return nil, false
	}

	return c.Cache.Get(key)
}

func (c *Client) cachePut(key string, value []byte, expires time.Time) {
	if c.Cache == nil {
		return
	}

	c.Cache.Put(key, value, expires)
}

func (c *Client) cacheDrop(key string) {
	if c.Cache == nil {
		return
	}

	c.Cache.Drop(key)
}

// Cache keys. The directory is in every key because one machine can talk
// to more than one installation, and a route or a ticket from one means
// nothing to the other.
//
// The credential's fingerprint is in every key too, as a second wall the
// caller cannot take down. NewDiskCache's identity is the first, but it
// is only the string the caller passed: a caller that hands the same
// identity to two different credentials - or an empty tenant to an
// identity that sits in several - would otherwise share tickets between
// them silently. Keying on the credential itself means the sharing cannot
// happen however the caller names things.
func (c *Client) ticketKey(namespace, verb string) string {
	return "ticket\x00" + c.credentialScope() + "\x00" + c.Directory + "\x00" + namespace + "\x00" + verb
}

func (c *Client) routeKey(namespace string) string {
	return "route\x00" + c.credentialScope() + "\x00" + c.Directory + "\x00" + namespace
}

// credentialScope fingerprints who is asking. It is a hash, not the
// credential: a cache key is written nowhere, but it is passed around,
// logged by a caller that wants to, and there is no reason for an API key
// to travel in one.
func (c *Client) credentialScope() string {
	cr := c.Credentials
	sum := sha256.Sum256([]byte(cr.Tenant + "\x00" + cr.Username + "\x00" + cr.APIKey + "\x00" + cr.KeyFile))
	return hex.EncodeToString(sum[:8])
}
