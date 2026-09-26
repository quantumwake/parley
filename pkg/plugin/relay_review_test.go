package plugin

import (
	"net/http"
	"os"
	"testing"
)

// With the relay on, a DIRECTORY request (resolve, ticket exchange) from the
// same client must still reach the directory, not come back 403 from the relay.
func TestTheRelayCarriesOnlyMemberRequests(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	os.Chmod(dir, 0o700)
	sock, ok := relaySocket(dir, "sess")
	if !ok {
		t.Fatal("socket path")
	}
	ln, err := serveRelay(sock, func(p string) (string, bool) {
		if namespaceOf(p) == "" {
			return "", false
		}
		return "https://member.example", true
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	rt := relayRoundTripper{dataDir: dir, session: "sess"}
	req, _ := http.NewRequest("GET", "https://directory.statefs.io/api/v1/cluster/route/ns-1", nil)
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Logf("err: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		body := make([]byte, 64)
		n, _ := resp.Body.Read(body)
		t.Fatalf("body %q: a directory resolve through the relayed client came back %d from the relay itself", body[:n], resp.StatusCode)
	}
}
