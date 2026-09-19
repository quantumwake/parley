package plugin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Version floors, both ways. statefs publishes its release and the oldest
// version of each client it accepts at GET <directory>/api/v1/version;
// parley declares the oldest statefs it supports. Either side being below
// the other's floor is a warning, never a stop: capture keeps running.

// MinServer is the oldest statefs parley supports: v0.5.27 carries the
// authenticator verify route statefs.ai's agent sign-in (people search)
// goes through.
const MinServer = "0.5.27"

// MinServerNeed says what stops working below MinServer; it changes with it.
const MinServerNeed = "people search in the console's grant field needs its authenticator verify route to sign in to statefs.ai"

// ClientName is parley's key in the directory's min_clients map, and the
// product in its User-Agent.
const ClientName = "parley"

// ClientVersion is this binary's version; main sets it at start.
var ClientVersion string

// UserAgent names parley and its version on outgoing calls, so a server can
// tell which clients are below its floor.
func UserAgent() string { return ClientName + "/" + strings.TrimSpace(ClientVersion) }

// ServerCheckTTL is how long a fetched answer is trusted.
const ServerCheckTTL = time.Hour

// ServerInfo is the directory's answer. Missing means the directory has no
// version route, which is a statefs older than the release that added it.
type ServerInfo struct {
	Version    string            `json:"version"`
	MinClients map[string]string `json:"min_clients"`
	Missing    bool              `json:"missing,omitempty"`
	FetchedMs  int64             `json:"fetched_ms"`
}

func serverInfoPath(env Env) string { return filepath.Join(env.DataDir, "server.json") }

// KnownServerInfo is the cached answer, without touching the network.
func KnownServerInfo(env Env) (ServerInfo, bool) {
	b, err := os.ReadFile(serverInfoPath(env))
	if err != nil {
		return ServerInfo{}, false
	}

	var s ServerInfo
	if json.Unmarshal(b, &s) != nil {
		return ServerInfo{}, false
	}

	return s, true
}

// CheckServer asks the directory for its version and client floors, caching
// the answer for ServerCheckTTL unless force. An unreachable directory is
// not an answer: the cache stays as it was and ok is false.
func CheckServer(ctx context.Context, env Env, force bool) (ServerInfo, bool) {
	if s, ok := KnownServerInfo(env); ok && !force && time.Since(time.UnixMilli(s.FetchedMs)) < ServerCheckTTL {
		return s, true
	}

	if env.Directory == "" {
		return ServerInfo{}, false
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(env.Directory, "/")+"/api/v1/version", nil)
	if err != nil {
		return ServerInfo{}, false
	}
	req.Header.Set("User-Agent", UserAgent())

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return ServerInfo{}, false
	}
	defer res.Body.Close()

	var s ServerInfo
	switch res.StatusCode {
	case http.StatusOK:
		if json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&s) != nil {
			return ServerInfo{}, false
		}
	case http.StatusNotFound:
		s.Missing = true
	default:
		return ServerInfo{}, false
	}

	s.FetchedMs = time.Now().UnixMilli()
	b, _ := json.Marshal(s)
	_ = os.WriteFile(serverInfoPath(env), b, 0o600)
	return s, true
}

// FloorNotices are the lines to show for s: parley below the server's floor
// for it, or the server below MinServer. A server with no version route
// says nothing either way; anything unparseable never nags.
func FloorNotices(s ServerInfo, current string) []string {
	if s.Missing {
		return nil
	}

	var out []string
	if floor := s.MinClients[ClientName]; NewerVersion(current, floor) {
		out = append(out, "parley "+strings.TrimSpace(current)+" is older than the "+floor+" this statefs requires; update with `claude plugin update parley@parley` and restart Claude Code (capture keeps running meanwhile)")
	}

	if NewerVersion(s.Version, MinServer) {
		out = append(out, "statefs "+strings.TrimPrefix(s.Version, "v")+" is older than the "+MinServer+" parley needs ("+MinServerNeed+"); capture keeps working")
	}

	return out
}
