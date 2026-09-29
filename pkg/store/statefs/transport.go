package statefs

// transport.go — the connections under the reads, and what a dead one costs.
//
// Everything this store sends — directory calls, member reads, the live
// tail — went through one http.Client on Go's default transport with a
// 30 s total timeout per request and nothing shorter underneath. Against
// the statefs ingress that transport negotiates HTTP/2, so every read of
// a member and every doorbell tail on it shared ONE connection. A load
// balancer that drops that connection without saying so leaves each read
// on it to its 30 s, the tail silent, and nothing to close it: Go's HTTP/2
// transport only gives a connection up on an error, and a dropped socket
// does not produce one until TCP gives up minutes later. One identity
// poller reads for every session on the machine, so one such connection
// left every session deaf for 30–60 s (general, 2026-09-28: `read head:
// … context deadline exceeded`, from a Mac whose fresh connects to the
// balancer were not completing either).
//
// So the store now has a transport of its own, per host, that
//
//   - speaks HTTP/1.1: one request per connection, so a dead one costs
//     its own request and the pool it came from can be dropped;
//   - has a deadline for each stage — dial, handshake, headers — well
//     under the whole, so a hung connection is known in seconds;
//   - drops every idle connection to a host the moment one request to it
//     times out, so the retry dials fresh instead of inheriting the pool;
//   - keeps an idle connection no longer than a balancer is likely to.
//
// The live tail is the one request that must NOT have a total timeout: it
// is meant to stay open for as long as its ticket lasts. It gets a
// read-idle deadline instead. The member says nothing while nothing lands
// except a `head` event every 15 s (MEMBER_EVENTS_HEARTBEAT; it sends no
// comment keepalives), a beat that can be a second late behind its idle
// poll. tailIdle is two of those: one late head is a busy member or a
// slow proxy, and tearing a healthy stream down for it every 20 s across
// every conversation on the machine is the reconnect storm the tail's
// backoff exists to prevent; two missed is a dead connection, whatever
// the socket says.

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// deadlines bound each stage of an ordinary request. Together they put a
// request to a dead host at about the header deadline, while a healthy
// one is never touched: a laptop reaches the balancer in well under a
// second at every stage. request is the whole of one, body included —
// what http.Client.Timeout was. Tests shorten them before New.
var deadlines = struct {
	dial, handshake, headers, idle, request time.Duration
}{
	dial:      3 * time.Second,
	handshake: 5 * time.Second,
	headers:   5 * time.Second,
	idle:      30 * time.Second,
	request:   30 * time.Second,
}

// tailIdle is how long a live tail may go without a byte before it is
// closed and opened again (the header above says why two heartbeats).
var tailIdle = 30 * time.Second

// ErrTailIdle ends a tail that went silent for tailIdle: the connection
// under it is presumed dead, and the caller opens another.
var ErrTailIdle = errors.New("live tail: no bytes for too long; the connection is presumed dead")

// idlePerHost is how many idle connections a host's pool keeps: as many
// as the poller reads at once (plugin scanParallel), so a round reuses
// the connections of the last one instead of dialling a burst of fresh
// ones every two seconds.
const idlePerHost = 8

// transport is one *http.Transport per host behind one RoundTripper, so
// a dead host's pool can be dropped on its own and a member that hangs
// costs the directory nothing.
type transport struct {
	mu     sync.Mutex
	byHost map[string]*http.Transport
	resets map[string]int // idle-connection drops per host; a test reads it
}

func newTransport() *transport {
	return &transport{byHost: map[string]*http.Transport{}, resets: map[string]int{}}
}

func (t *transport) forHost(host string) *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	tr := t.byHost[host]
	if tr == nil {
		tr = &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: deadlines.dial, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   deadlines.handshake,
			ResponseHeaderTimeout: deadlines.headers,
			IdleConnTimeout:       deadlines.idle,
			ExpectContinueTimeout: time.Second,
			MaxIdleConnsPerHost:   idlePerHost,
			// HTTP/1.1 only: an empty TLSNextProto is how the transport
			// is told not to negotiate h2 (net/http's own documented way).
			TLSNextProto: map[string]func(string, *tls.Conn) http.RoundTripper{},
		}
		t.byHost[host] = tr
	}

	return tr
}

// Reset drops every idle connection to host, so the next request dials
// fresh. "" drops them all: a laptop that woke up has no live connection
// to anything.
func (t *transport) Reset(host string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for h, tr := range t.byHost {
		if host == "" || h == host {
			tr.CloseIdleConnections()
			t.resets[h]++
		}
	}
}

func (t *transport) resetCount(host string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.resets[host]
}

// RoundTrip sends one request on its host's transport. An ordinary request
// is bounded to deadlines.request as a whole; a tail (Accept:
// text/event-stream) is bounded only by silence. A timeout of either kind
// drops the host's idle pool, because a connection that hung is unlikely
// to be the only one that has.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	host := req.URL.Host
	tr := t.forHost(host)
	if req.Header.Get("Accept") == "text/event-stream" {
		ctx, cancel := context.WithCancel(req.Context())
		resp, err := tr.RoundTrip(req.WithContext(ctx))
		if err != nil {
			cancel()
			if timedOut(err) {
				t.Reset(host)
			}

			return nil, err
		}

		resp.Body = newIdleBody(resp.Body, tailIdle, cancel, func() { t.Reset(host) })
		return resp, nil
	}

	ctx, cancel := context.WithTimeout(req.Context(), deadlines.request)
	resp, err := tr.RoundTrip(req.WithContext(ctx))
	if err != nil {
		cancel()
		if timedOut(err) {
			t.Reset(host)
		}

		return nil, err
	}

	resp.Body = &boundedBody{rc: resp.Body, ctx: ctx, cancel: cancel, onTimeout: func() { t.Reset(host) }}
	return resp, nil
}

// timedOut reports a request that ran out of time at any stage: the dial,
// the handshake, the headers, or a deadline on the caller's context. A
// cancelled context is not this.
func timedOut(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// boundedBody is an ordinary response's body under the request's
// deadline: reading past it fails, and closing it lets the deadline go.
type boundedBody struct {
	rc        io.ReadCloser
	ctx       context.Context
	cancel    context.CancelFunc
	onTimeout func()
	once      sync.Once
}

func (b *boundedBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if err != nil && !errors.Is(err, io.EOF) && errors.Is(b.ctx.Err(), context.DeadlineExceeded) {
		b.once.Do(b.onTimeout)
	}

	return n, err
}

func (b *boundedBody) Close() error {
	err := b.rc.Close()
	b.cancel()
	return err
}

// idleBody is a tail's body with a silence deadline: a stretch of idle
// longer than it ends the request, and the read that was waiting answers
// ErrTailIdle so the caller knows to open another rather than to back
// off. Any byte — a row, a head, a comment — starts the stretch again.
type idleBody struct {
	rc     io.ReadCloser
	idle   time.Duration
	cancel context.CancelFunc
	timer  *time.Timer

	mu     sync.Mutex
	fired  bool
	closed bool
}

func newIdleBody(rc io.ReadCloser, idle time.Duration, cancel context.CancelFunc, onIdle func()) *idleBody {
	b := &idleBody{rc: rc, idle: idle, cancel: cancel}
	b.timer = time.AfterFunc(idle, func() {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			return
		}

		b.fired = true
		b.mu.Unlock()
		cancel() // the transport ends the read that is waiting
		onIdle()
	})

	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	b.mu.Lock()
	fired := b.fired
	if n > 0 && !fired && !b.closed {
		b.timer.Reset(b.idle)
	}
	b.mu.Unlock()
	if fired {
		return n, ErrTailIdle
	}

	return n, err
}

func (b *idleBody) Close() error {
	b.mu.Lock()
	b.closed = true
	b.timer.Stop()
	b.mu.Unlock()
	err := b.rc.Close()
	b.cancel()
	return err
}
