package statefs

import (
	"context"
	"net/url"
	"testing"
	"time"
)

// A member the reads have not heard from is beaten with the cheapest read
// there is, under its own short deadline. A miss answers false and drops
// the host's pool, so the next real read dials fresh instead of waiting
// its own deadline out on the same dead connection.
func TestABeatMissDropsTheHostsPool(t *testing.T) {
	shortDeadlines(t)
	srv := newHangingServer(t)
	st := New(Config{Directory: "http://directory.invalid"})
	host := hostOf(srv.URL)

	// One read that answered leaves the host known and a connection
	// pooled; then the balancer drops it and nothing has been heard since.
	if _, err := get(t, st.Client().HTTP, srv.URL+"/a"); err != nil {
		t.Fatal(err)
	}

	st.members.note(srv.URL, "ns", nil)
	srv.startHanging()
	st.members.seen[host].heard = time.Now().Add(-time.Minute)

	start := time.Now()
	out := st.Beat(context.Background(), 15*time.Second, 100*time.Millisecond)
	if ok, known := out[host]; !known || ok {
		t.Fatalf("a member that hangs is a miss, got %v", out)
	}

	if took := time.Since(start); took > time.Second {
		t.Fatalf("a beat is bounded by its own deadline, took %s", took)
	}

	if n := st.transport.resetCount(host); n < 1 {
		t.Fatal("a miss must drop the host's pool")
	}

	// The proof the pool is gone: the next read is answered on a fresh
	// connection, at once.
	if _, err := get(t, st.Client().HTTP, srv.URL+"/b"); err != nil {
		t.Fatalf("the read after a missed beat must dial fresh and be answered, got %v", err)
	}

	if addr := srv.lastAnswered(); srv.sawBefore(addr) {
		t.Fatalf("the read after a missed beat reused pooled connection %s", addr)
	}
}

// No extra load in the normal case: a host the reads heard from within
// the interval is not beaten at all, and one already beaten within the
// interval is not beaten again.
func TestABeatIsSkippedForAHostTheReadsJustHeard(t *testing.T) {
	srv := newHangingServer(t)
	st := New(Config{Directory: "http://directory.invalid"})
	host := hostOf(srv.URL)

	st.members.note(srv.URL, "ns", nil)
	out := st.Beat(context.Background(), 15*time.Second, time.Second)
	if !out[host] {
		t.Fatalf("a host just heard from is there: %v", out)
	}

	if n := srv.answers(); n != 0 {
		t.Fatalf("a host the reads just heard from must not be beaten, yet it answered %d requests", n)
	}

	// Silent for longer than the interval: one beat, answered.
	st.members.seen[host].heard = time.Now().Add(-time.Minute)
	out = st.Beat(context.Background(), 15*time.Second, time.Second)
	if !out[host] || srv.answers() != 1 {
		t.Fatalf("a silent host is beaten once and answers: %v after %d requests", out, srv.answers())
	}

	// Answered, so heard: not beaten again.
	st.Beat(context.Background(), 15*time.Second, time.Second)
	if n := srv.answers(); n != 1 {
		t.Fatalf("a host that answered its beat is heard from, yet it was beaten again: %d requests", n)
	}
}

// A refusal is an answer: the host is there. Only a request that never
// completed is a miss.
func TestARefusalIsNotAMiss(t *testing.T) {
	if unanswered(context.DeadlineExceeded) != true {
		t.Fatal("a deadline is a miss")
	}

	if unanswered(&url.Error{Op: "Get", URL: "https://m/", Err: context.DeadlineExceeded}) != true {
		t.Fatal("a transport error is a miss")
	}

	if unanswered(errRefusedForTest) {
		t.Fatal("an HTTP status is an answer")
	}
}

var errRefusedForTest = &statusError{}

type statusError struct{}

func (*statusError) Error() string { return "GET /api/v1/state/ns: HTTP 403: no grant" }

// A member nothing reads any more drops off the list, so it is not beaten
// for a conversation this machine stopped following.
func TestAMemberNotReadForAMinuteIsForgotten(t *testing.T) {
	st := New(Config{Directory: "http://directory.invalid"})
	st.members.note("https://gone.example", "ns", nil)
	st.members.seen["gone.example"].asked = time.Now().Add(-2 * memberForget)
	out := st.Beat(context.Background(), 15*time.Second, time.Second)
	if _, known := out["gone.example"]; known {
		t.Fatalf("a member not read for %s must be forgotten: %v", memberForget, out)
	}

	if hosts := st.Hosts(); len(hosts) != 0 {
		t.Fatalf("forgotten means gone from the list: %v", hosts)
	}
}
