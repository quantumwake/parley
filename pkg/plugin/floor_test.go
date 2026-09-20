package plugin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFloorNoticesBothWays(t *testing.T) {
	cases := []struct {
		name    string
		info    ServerInfo
		current string
		want    []string // substrings, one per notice
	}{
		{"both fine", ServerInfo{Version: "v0.5.28", MinClients: map[string]string{"parley": "0.3.17"}}, "0.3.18", nil},
		{"parley below its floor", ServerInfo{Version: "v0.5.28", MinClients: map[string]string{"parley": "0.3.18"}}, "0.3.17", []string{"parley 0.3.17 is older than the 0.3.18"}},
		{"server below parley's floor", ServerInfo{Version: "v0.5.26"}, "0.3.18", []string{"statefs 0.5.26 is older than the " + MinServer}},
		{"both below", ServerInfo{Version: "0.5.20", MinClients: map[string]string{"parley": "9.0.0"}}, "0.3.18", []string{"parley 0.3.18 is older", "statefs 0.5.20 is older"}},
		{"no floor for parley", ServerInfo{Version: "v0.5.28", MinClients: map[string]string{"statefs.ai": "1.0.0"}}, "0.3.18", nil},
		{"no version route", ServerInfo{Missing: true}, "0.0.1", nil},
		{"unparseable never nags", ServerInfo{Version: "dev", MinClients: map[string]string{"parley": "latest"}}, "0.3.18", nil},
	}

	for _, c := range cases {
		got := FloorNotices(c.info, c.current)
		if len(got) != len(c.want) {
			t.Fatalf("%s: %q", c.name, got)
		}
		for i, w := range c.want {
			if !strings.Contains(got[i], w) {
				t.Fatalf("%s: %q lacks %q", c.name, got[i], w)
			}
		}
	}
}

func TestCheckServerAsksCachesAndSaysWhoItIs(t *testing.T) {
	var hits atomic.Int32
	var agent atomic.Value
	status := http.StatusOK
	dir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		agent.Store(r.Header.Get("User-Agent"))
		if r.URL.Path != "/api/v1/version" {
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"version":"v0.5.28","min_clients":{"parley":"0.3.17"}}`))
	}))
	defer dir.Close()

	ClientVersion = "0.3.18"
	env := Env{Directory: dir.URL, DataDir: t.TempDir()}
	s, ok := CheckServer(context.Background(), env, false)
	if !ok || s.Version != "v0.5.28" || s.MinClients["parley"] != "0.3.17" {
		t.Fatalf("%+v %v", s, ok)
	}
	if agent.Load() != "parley/0.3.18" {
		t.Fatalf("User-Agent %v", agent.Load())
	}

	if _, ok := CheckServer(context.Background(), env, false); !ok || hits.Load() != 1 {
		t.Fatalf("a fresh answer comes from the cache: %d requests", hits.Load())
	}

	status = http.StatusInternalServerError
	if s, ok := CheckServer(context.Background(), env, true); ok || s.Version != "" {
		t.Fatalf("an error is not an answer: %+v %v", s, ok)
	}
	if k, ok := KnownServerInfo(env); !ok || k.Version != "v0.5.28" {
		t.Fatalf("a failed check keeps the last answer: %+v", k)
	}
}

func TestCheckServerTreatsNoRouteAsOlderStatefs(t *testing.T) {
	dir := httptest.NewServer(http.NotFoundHandler())
	defer dir.Close()

	s, ok := CheckServer(context.Background(), Env{Directory: dir.URL, DataDir: t.TempDir()}, true)
	if !ok || !s.Missing || len(FloorNotices(s, "0.0.1")) != 0 {
		t.Fatalf("%+v %v", s, ok)
	}
}

func TestCheckServerWithoutDirectoryIsNoAnswer(t *testing.T) {
	if _, ok := CheckServer(context.Background(), Env{DataDir: t.TempDir()}, true); ok {
		t.Fatal("no directory, no answer")
	}
}
