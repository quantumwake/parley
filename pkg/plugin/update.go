package plugin

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UpdateCheckTTL is how long a fetched answer is trusted. Version checks are
// a convenience, never a reason to make a hook or a command slower.
const UpdateCheckTTL = 24 * time.Hour

type updateCache struct {
	Latest    string `json:"latest"`
	FetchedMs int64  `json:"fetched_ms"`
}

func updateCachePath(env Env) string { return filepath.Join(env.DataDir, "latest.json") }

// LatestKnown returns the newest release seen, from cache only. It never
// touches the network, so it is safe anywhere.
func LatestKnown(env Env) string {
	b, err := os.ReadFile(updateCachePath(env))
	if err != nil {
		return ""
	}

	var c updateCache
	if json.Unmarshal(b, &c) != nil {
		return ""
	}

	return c.Latest
}

// CheckLatest asks GitHub for the newest release and caches it. The repo is
// private, so it goes through `gh`, which already holds the user's auth; with
// no gh, there is no answer and that is not an error worth reporting.
func CheckLatest(ctx context.Context, env Env, force bool) (string, error) {
	if !force {
		if b, err := os.ReadFile(updateCachePath(env)); err == nil {
			var c updateCache
			if json.Unmarshal(b, &c) == nil && time.Since(time.UnixMilli(c.FetchedMs)) < UpdateCheckTTL {
				return c.Latest, nil
			}
		}
	}

	gh, err := exec.LookPath("gh")
	if err != nil {
		return "", nil
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, gh, "release", "view",
		"-R", "quantumwake/statefs.ai", "--json", "tagName", "-q", ".tagName").Output()
	if err != nil {
		return "", nil // no releases, no auth, offline: all silent
	}

	latest := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if latest == "" {
		return "", nil
	}

	b, _ := json.Marshal(updateCache{Latest: latest, FetchedMs: time.Now().UnixMilli()})
	_ = os.WriteFile(updateCachePath(env), b, 0o600)
	return latest, nil
}

// NewerVersion answers whether b is a higher version than a. Both are dotted
// numbers; anything unparseable compares as not newer, so a malformed answer
// never nags.
func NewerVersion(a, b string) bool {
	pa, pb := parseVersion(a), parseVersion(b)
	if pa == nil || pb == nil {
		return false
	}

	for i := 0; i < 3; i++ {
		if pb[i] != pa[i] {
			return pb[i] > pa[i]
		}
	}

	return false
}

func parseVersion(v string) []int {
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".", 4)
	if len(parts) < 3 {
		return nil
	}

	out := make([]int, 3)
	for i := 0; i < 3; i++ {
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return nil
		}

		out[i] = n
	}

	return out
}

// UpdateNotice is the one line to show when a newer release is known, or "".
func UpdateNotice(env Env, current string) string {
	latest := LatestKnown(env)
	if !NewerVersion(current, latest) {
		return ""
	}

	return "update available: " + latest + " (you have " + strings.TrimSpace(current) + "); run `parley version --check` or update the plugin"
}
