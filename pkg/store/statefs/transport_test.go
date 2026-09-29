package statefs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// hangingServer answers at once until told to hang. From then on a
// request that arrives on a connection it has seen before hangs — the
// shape of a balancer that dropped a pooled connection without saying so
// — and only a request on a fresh connection is answered.
type hangingServer struct {
	*httptest.Server
	mu       sync.Mutex
	known    map[string]bool // connections seen before the hang began
	hang     bool
	answered []string // the connection of each answered request, in order
}

func newHangingServer(t *testing.T) *hangingServer {
	t.Helper()
	s := &hangingServer{known: map[string]bool{}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		hang := s.hang && s.known[r.RemoteAddr]
		if !s.hang {
			s.known[r.RemoteAddr] = true
		}
		if !hang {
			s.answered = append(s.answered, r.RemoteAddr)
		}
		s.mu.Unlock()

		if hang {
			<-r.Context().Done()
			return
		}

		// Long enough that two requests at once need two connections.
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(`{"total_rows":0}`))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *hangingServer) startHanging() {
	s.mu.Lock()
	s.hang = true
	s.mu.Unlock()
}

func (s *hangingServer) sawBefore(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.known[addr]
}

func (s *hangingServer) lastAnswered() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.answered) == 0 {
		return ""
	}

	return s.answered[len(s.answered)-1]
}

func (s *hangingServer) answers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.answered)
}

// shortDeadlines makes a hung request fail in a fraction of a second
// rather than the five the real transport allows, so the suite stays
// quick; the shape of what happens is the same.
func shortDeadlines(t *testing.T) {
	t.Helper()
	prev := deadlines
	deadlines.headers = 200 * time.Millisecond
	deadlines.request = time.Second
	t.Cleanup(func() { deadlines = prev })
}

func get(t *testing.T, hc *http.Client, url string) (string, error) {
	t.Helper()
	resp, err := hc.Get(url)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Request.URL.Host, nil
}

// The point of the reset: with two pooled connections to a host and the
// balancer having dropped both, the first read hangs to its deadline and
// fails. Without the reset the second read takes the OTHER pooled
// connection and hangs for the same deadline again — every read paying
// the timeout once per dead connection. With it the pool is gone the
// moment the first read times out, and the next one dials fresh and is
// answered. In production that is about the 5 s header deadline; the
// production deadlines are checked below so this test can be quick.
func TestATimeoutDropsThePoolAndTheNextReadDialsFresh(t *testing.T) {
	shortDeadlines(t)
	srv := newHangingServer(t)
	st := New(Config{Directory: "http://directory.invalid"})
	hc := st.Client().HTTP

	// Two reads at once leave two connections in the pool.
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := get(t, hc, srv.URL+"/a"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := srv.answers(); n != 2 {
		t.Fatalf("two reads at once should be two answers, got %d", n)
	}

	srv.startHanging()
	start := time.Now()
	_, err := get(t, hc, srv.URL+"/b")
	if err == nil || !timedOut(err) {
		t.Fatalf("a read on a dropped connection must time out, got %v", err)
	}

	if _, err := get(t, hc, srv.URL+"/c"); err != nil {
		t.Fatalf("the read after a timeout must be answered on a fresh connection, got %v", err)
	}

	took := time.Since(start)
	if took > 2*deadlines.headers+500*time.Millisecond {
		t.Fatalf("the timeout and the fresh read took %s: the second read waited on the pool too", took)
	}

	if addr := srv.lastAnswered(); srv.sawBefore(addr) {
		t.Fatalf("the read after a timeout reused pooled connection %s instead of dialling fresh", addr)
	}

	host := hostOf(srv.URL)
	if n := st.transport.resetCount(host); n != 1 {
		t.Fatalf("one timeout is one reset of the host's pool, got %d", n)
	}
}

// The deadlines this stands on, as documented in transport.go: a hung
// dial or a hung reused connection is known in seconds, not in the 30 s
// the client's timeout used to allow.
func TestTheProductionDeadlinesAreShort(t *testing.T) {
	if deadlines.dial > 3*time.Second || deadlines.handshake > 5*time.Second || deadlines.headers > 5*time.Second {
		t.Fatalf("a hung connection must be known within about 6 s: %+v", deadlines)
	}

	if deadlines.request != 30*time.Second {
		t.Fatalf("the whole of an ordinary request keeps the 30 s it had, got %s", deadlines.request)
	}

	if deadlines.idle != 30*time.Second {
		t.Fatalf("an idle connection is not kept past 30 s, got %s", deadlines.idle)
	}
}

// A host that hangs does not touch another host's pool.
func TestAResetIsPerHost(t *testing.T) {
	tr := newTransport()
	tr.forHost("a.example:443")
	tr.forHost("b.example:443")
	tr.Reset("a.example:443")
	if tr.resetCount("a.example:443") != 1 || tr.resetCount("b.example:443") != 0 {
		t.Fatalf("resetting a must not reset b: %v", tr.resets)
	}

	tr.Reset("")
	if tr.resetCount("a.example:443") != 2 || tr.resetCount("b.example:443") != 1 {
		t.Fatalf("resetting \"\" resets every host: %v", tr.resets)
	}
}

// tailServer streams one head event, then does what the test says: goes
// silent, or keeps sending.
func tailServer(t *testing.T, keepSending time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_, _ = fmt.Fprint(w, "event: head\ndata: {\"head\":0}\n\n")
		_ = rc.Flush()
		if keepSending > 0 {
			until := time.Now().Add(keepSending)
			for time.Now().Before(until) {
				select {
				case <-r.Context().Done():
					return
				case <-time.After(30 * time.Millisecond):
				}

				_, _ = fmt.Fprint(w, ": keepalive\n\n")
				_ = rc.Flush()
			}

			return
		}

		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

func openTail(t *testing.T, st *Store, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Accept", "text/event-stream")
	resp, err := st.Client().HTTP.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// readAll reads the body until it ends, and answers what ended it and
// how long it took. It fails the test rather than hang forever.
func readAll(t *testing.T, body io.Reader, bound time.Duration) (string, error, time.Duration) {
	t.Helper()
	type end struct {
		got string
		err error
	}
	done := make(chan end, 1)
	start := time.Now()
	go func() {
		b, err := io.ReadAll(body)
		done <- end{string(b), err}
	}()

	select {
	case e := <-done:
		return e.got, e.err, time.Since(start)
	case <-time.After(bound):
		t.Fatalf("the stream did not end within %s", bound)
		return "", nil, 0
	}
}

// A tail with no byte for tailIdle is ended as idle, and the host's pool
// is dropped with it: a silent stream and a dead connection look the same
// from here, and the tail's caller opens another. The member's head every
// 15 s (its idle heartbeat) is what keeps a healthy stream under tailIdle.
func TestATailThatGoesSilentIsEndedAsIdle(t *testing.T) {
	prev := tailIdle
	tailIdle = 150 * time.Millisecond
	t.Cleanup(func() { tailIdle = prev })

	srv := tailServer(t, 0)
	st := New(Config{Directory: "http://directory.invalid"})
	resp := openTail(t, st, srv.URL+"/api/v1/state/ns/events")

	got, err, took := readAll(t, resp.Body, 5*time.Second)
	if !errors.Is(err, ErrTailIdle) {
		t.Fatalf("a silent stream must end as ErrTailIdle, got %v", err)
	}

	if !strings.Contains(got, "event: head") {
		t.Fatalf("the bytes before the silence are still delivered: %q", got)
	}

	if took < tailIdle || took > 5*tailIdle {
		t.Fatalf("the silence deadline is about %s, the stream ended after %s", tailIdle, took)
	}

	if n := st.transport.resetCount(hostOf(srv.URL)); n != 1 {
		t.Fatalf("an idle tail drops its host's pool once, got %d resets", n)
	}
}

// Any byte keeps a tail open — a comment keepalive as much as a row — and
// a tail is not bounded as a whole: one that keeps sending for longer
// than an ordinary request may take is not cut.
func TestATailThatKeepsSendingIsNotCut(t *testing.T) {
	prev := tailIdle
	tailIdle = 100 * time.Millisecond
	t.Cleanup(func() { tailIdle = prev })
	shortDeadlines(t) // deadlines.request is 1 s here; the stream outlives it

	srv := tailServer(t, 1200*time.Millisecond)
	st := New(Config{Directory: "http://directory.invalid"})
	resp := openTail(t, st, srv.URL+"/api/v1/state/ns/events")

	got, err, took := readAll(t, resp.Body, 5*time.Second)
	if err != nil {
		t.Fatalf("a stream that keeps sending ends cleanly when the member ends it, got %v after %s", err, took)
	}

	if took < time.Second {
		t.Fatalf("the stream ended after %s; it should have outlived the 1 s request deadline", took)
	}

	if strings.Count(got, ": keepalive") < 10 {
		t.Fatalf("expected the keepalives to keep coming: %q", got)
	}
}

// An ordinary request is still bounded as a whole: a body that trickles
// past deadlines.request fails, and drops the pool.
func TestAnOrdinaryRequestIsBoundedAsAWhole(t *testing.T) {
	shortDeadlines(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}

			_, _ = w.Write([]byte("x"))
			_ = rc.Flush()
		}
	}))
	t.Cleanup(srv.Close)

	st := New(Config{Directory: "http://directory.invalid"})
	resp, err := st.Client().HTTP.Get(srv.URL + "/trickle")
	if err != nil {
		t.Fatal(err)
	}

	defer resp.Body.Close()
	_, err, took := readAll(t, resp.Body, 5*time.Second)
	if err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("a trickling body must fail at the request deadline, got %v", err)
	}

	// The deadline runs from the request, a little before this read began.
	if took < deadlines.request/2 || took > 3*deadlines.request {
		t.Fatalf("the request deadline is %s, the body ended after %s", deadlines.request, took)
	}

	if n := st.transport.resetCount(hostOf(srv.URL)); n != 1 {
		t.Fatalf("a body that timed out drops the pool once, got %d", n)
	}
}

func TestTimedOutTellsATimeoutFromACancel(t *testing.T) {
	if timedOut(&url.Error{Err: context.Canceled}) {
		t.Fatal("a cancelled request is not a timeout")
	}

	if !timedOut(&url.Error{Err: context.DeadlineExceeded}) {
		t.Fatal("a deadline on the caller's context is a timeout")
	}
}

// The two things that would quietly undo transport.go if put back.
//
// First, the client's own timeout. http.Client.Timeout bounds the body
// as well as the headers, so a client with one cuts a healthy tail at
// that mark, as a fault: that is what the old 30 s did to every tail.
// The tail's only bound is silence, so the client that carries it must
// have no whole-request timeout at all; the request deadline lives in
// the transport, where the tail can be told apart.
func TestTheClientThatCarriesTheTailHasNoWholeRequestTimeout(t *testing.T) {
	st := New(Config{Directory: "http://directory.invalid"})
	if d := st.Client().HTTP.Timeout; d != 0 {
		t.Fatalf("the client's Timeout would cut every tail at %s; it must be 0, with ordinary requests bounded by the transport", d)
	}

	if _, ok := st.Client().HTTP.Transport.(*transport); !ok {
		t.Fatalf("the client must send through the store's transport, got %T", st.Client().HTTP.Transport)
	}
}

// Second, HTTP/2. The ingress offers it, and Go's default transport takes
// it: every read and every tail to a member then share one connection,
// which is the shape this whole file exists to end. Against a server
// that offers h2 over TLS, the member transport must still speak
// HTTP/1.1.
func TestTheMemberTransportSpeaksHTTP1ToAnH2Server(t *testing.T) {
	var mu sync.Mutex
	var protos []int
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		protos = append(protos, r.ProtoMajor)
		mu.Unlock()
		_, _ = w.Write([]byte("ok"))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)

	// The store's transport for this host, trusting the test certificate;
	// the protocol choice is the transport's own and is not touched.
	st := New(Config{Directory: "http://directory.invalid"})
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	st.transport.forHost(hostOf(srv.URL)).TLSClientConfig = &tls.Config{RootCAs: pool}

	// The control: the default transport, trusting the same certificate,
	// takes h2 from this server. If it did not, the test would prove nothing.
	control := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}
	if _, err := get(t, control, srv.URL+"/control"); err != nil {
		t.Fatal(err)
	}

	if _, err := get(t, st.Client().HTTP, srv.URL+"/member"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(protos) != 2 || protos[0] != 2 {
		t.Fatalf("the server must offer h2 and the control must take it: protocols %v", protos)
	}

	if protos[1] != 1 {
		t.Fatalf("the member transport spoke HTTP/%d; it must speak HTTP/1.1 so a dead connection costs one request", protos[1])
	}
}
