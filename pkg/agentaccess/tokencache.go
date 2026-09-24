package agentaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// A token cache on disk.
//
// Every parley command is its own process: a hook, a `parley presence` ping,
// a `parley post`. A token held only in memory dies with the process, so a
// machine that pings presence every few seconds signs in every few seconds —
// in production one mint per ping, 666 mints against 665 pings, ~26ms each on
// the critical path of a post.
//
// So the bearer is kept in a file, keyed by the host and the identity that
// earned it, and reused until it is close to expiring. The trade-off is real:
// a 15-minute bearer now sits on disk next to the identity's private key,
// which is the stronger secret and is already there. The file is 0600 in a
// 0700 directory, is ignored if its mode has been widened, and is dropped the
// moment statefs.ai refuses the token.
type cached struct {
	Base      string    `json:"base"`
	Username  string    `json:"username"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// cachePath is <CacheDir>/tokens/<hash>.json, where the hash covers the host
// and the identity: a second identity, or the same identity against another
// statefs.ai, never reads this one's token.
func (c *Client) cachePath() string {
	if c.CacheDir == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(c.Base + "\n" + c.Username))
	return filepath.Join(c.CacheDir, "tokens", hex.EncodeToString(sum[:8])+".json")
}

// readCache answers a token that is this client's and still good, or "".
func (c *Client) readCache() (string, time.Time) {
	path := c.cachePath()
	if path == "" {
		return "", time.Time{}
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", time.Time{}
	}

	// A widened file is not trusted: someone else can read it, so the token
	// in it is treated as compromised rather than reused.
	if info.Mode().Perm()&0o077 != 0 {
		_ = os.Remove(path)
		return "", time.Time{}
	}

	b, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}
	}

	var v cached
	if err := json.Unmarshal(b, &v); err != nil {
		return "", time.Time{}
	}

	// The hash should already rule this out; the fields are checked anyway,
	// so a collision or a hand-edited file cannot hand over a token that was
	// earned somewhere else.
	if v.Token == "" || v.Base != c.Base || v.Username != c.Username {
		return "", time.Time{}
	}

	if !c.now().Add(renewBefore).Before(v.ExpiresAt) {
		return "", time.Time{}
	}

	return v.Token, v.ExpiresAt
}

// writeCache stores a fresh token. Any failure is silent: the cache is a
// saving, never a requirement, and a process that cannot write one simply
// signs in again next time.
func (c *Client) writeCache(token string, expires time.Time) {
	path := c.cachePath()
	if path == "" || token == "" {
		return
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}

	b, err := json.Marshal(cached{Base: c.Base, Username: c.Username, Token: token, ExpiresAt: expires})
	if err != nil {
		return
	}

	// Written beside the target and renamed, so a reader never sees half a
	// file and two processes signing in at once cannot mix their tokens.
	tmp, err := os.CreateTemp(dir, ".token-*")
	if err != nil {
		return
	}

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return
	}

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		_ = os.Remove(tmp.Name())
		return
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
	}
}

// dropCache removes a refused token, so the next process does not present it
// again. It only removes a file holding that same token: another process may
// have signed in since.
func (c *Client) dropCache(token string) {
	path := c.cachePath()
	if path == "" {
		return
	}

	if have, _ := c.readCache(); have != "" && have != token {
		return
	}

	_ = os.Remove(path)
}
