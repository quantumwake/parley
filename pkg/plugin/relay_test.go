package plugin

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/quantumwake/parley/pkg/store"
)

// shortDir is under /tmp because a socket path has to fit in 103 bytes
// and t.TempDir does not.
func shortDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("/tmp", fmt.Sprintf("pr%x", os.Getpid()))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestRelayRefusesAWorldWritableDirectory(t *testing.T) {
	dir := shortDir(t)
	r := filepath.Join(dir, "r")
	if err := os.Mkdir(r, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.Chmod(r, 0o777); err != nil {
		t.Fatal(err)
	}

	ln, err := listenUnix(filepath.Join(r, "a.sock"))
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()
	if relayPathTrusted(dir, ln.Addr().String()) {
		t.Fatal("a world-writable directory was trusted")
	}
}

func TestRelayRefusesASymlinkedSocket(t *testing.T) {
	dir := shortDir(t)
	r := filepath.Join(dir, "r")
	if err := os.Mkdir(r, 0o700); err != nil {
		t.Fatal(err)
	}

	ln, err := listenUnix(filepath.Join(r, "real.sock"))
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()
	link := filepath.Join(r, "link.sock")
	if err := os.Symlink("real.sock", link); err != nil {
		t.Fatal(err)
	}

	if relayPathTrusted(dir, link) {
		t.Fatal("a symlinked socket was trusted")
	}
}

func TestRelayRefusesAnAbstractName(t *testing.T) {
	if _, err := listenUnix("@parley"); err == nil {
		t.Fatal("an abstract socket was accepted")
	}

	if _, ok := relaySocket(t.TempDir(), ""); ok {
		t.Fatal("an empty session got a socket")
	}
}

func TestRelayForwardsToTheAllowlistAndDoesNotFollowRedirects(t *testing.T) {
	dir := shortDir(t)
	landed := false
	evil := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		landed = true
	}))
	defer evil.Close()

	member := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host == "evil.example" {
			t.Errorf("the daemon used the request Host %q", r.Host)
		}

		if r.URL.Path == "/api/v1/state/ns/append" {
			http.Redirect(w, r, evil.URL+"/landed", http.StatusTemporaryRedirect)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "member")
	}))
	defer member.Close()

	path := filepath.Join(dir, "r", "one.sock")
	prev := relayClient
	c := member.Client()
	c.CheckRedirect = relayClient.CheckRedirect
	relayClient = c
	t.Cleanup(func() { relayClient = prev })
	ln, err := serveRelay(path, func(p string) (string, bool) {
		if p == "/api/v1/state/ns/append" || p == "/api/v1/state/ns/scan" {
			return member.URL, true
		}

		return "", false
	})
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()

	get := func(urlPath, host string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://parley-relay"+urlPath, nil)
		if err != nil {
			t.Fatal(err)
		}

		req.Host = host
		tr := &http.Transport{DialContext: dialUnix(path)}
		resp, err := tr.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}

		return resp
	}

	resp := get("/api/v1/state/ns/scan", "evil.example")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "member" || landed {
		t.Fatalf("scan: status %d body %q evil-hit %v", resp.StatusCode, body, landed)
	}

	resp = get("/api/v1/state/ns/append", "evil.example")
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect || landed {
		t.Fatalf("redirect was not handed back: %d evil-hit %v", resp.StatusCode, landed)
	}
}

func TestRelayRefusesAForeignPeer(t *testing.T) {
	dir := shortDir(t)
	path := filepath.Join(dir, "r", "one.sock")
	ln, err := serveRelay(path, func(string) (string, bool) {
		return "https://member.example", true
	})
	if err != nil {
		t.Fatal(err)
	}

	defer ln.Close()
	prev := relayPeerOK
	relayPeerOK = func(*http.Request) bool { return false }
	t.Cleanup(func() { relayPeerOK = prev })

	req, _ := http.NewRequest(http.MethodGet, "http://parley-relay/api/v1/state/ns/scan", nil)
	resp, err := (&http.Transport{DialContext: dialUnix(path)}).RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("a foreign peer was served: %d", resp.StatusCode)
	}
}

func dialUnix(path string) func(context.Context, string, string) (net.Conn, error) {
	return func(context.Context, string, string) (net.Conn, error) {
		return net.Dial("unix", path)
	}
}

type cursorStore struct {
	*store.Fake
	from store.Position
}

func (c *cursorStore) Ring(_ context.Context, _ string, from store.Position) (<-chan struct{}, error) {
	c.from = from
	ch := make(chan struct{})
	close(ch)
	return ch, nil
}

func TestTheLiveTailResumesFromTheStoredCursor(t *testing.T) {
	t.Setenv("PARLEY_FEATURES", "doorbell")
	st := &cursorStore{Fake: store.NewFake()}
	_, stop := startBells(t.Context(), Env{}, st, []Subscription{{ID: "ns", Name: "general", Cursor: 40}})
	stop()
	if st.from != 40 {
		t.Fatalf("the tail opened at %d; the stored cursor is 40, and the head is not", st.from)
	}
}
