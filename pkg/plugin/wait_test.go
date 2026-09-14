package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

// failingStore answers Scan with err while fail is set, and passes through
// otherwise.
type failingStore struct {
	store.Store
	fail atomic.Bool
	err  error
}

func (f *failingStore) Scan(ctx context.Context, ns string, from, to store.Position) iter.Seq2[event.Event, error] {
	if f.fail.Load() {
		return func(yield func(event.Event, error) bool) { yield(event.Event{}, f.err) }
	}

	return f.Store.Scan(ctx, ns, from, to)
}

// withWaitStore makes waits in this test read through a failing store.
func withWaitStore(t *testing.T, err error) *failingStore {
	t.Helper()
	fs := &failingStore{err: err}
	prev, prevPoll := waitStore, WaitPoll
	waitStore = func(env Env) (store.Store, error) {
		st, e := StoreFromEnv(env)
		fs.Store = st
		return fs, e
	}

	WaitPoll = 20 * time.Millisecond
	t.Cleanup(func() { waitStore, WaitPoll = prev, prevPoll })
	return fs
}

func followIssues(t *testing.T) Env {
	t.Helper()
	ctx := context.Background()
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	var out bytes.Buffer
	if err := CreateShared(ctx, a, "issues", "", nil, &out); err != nil {
		t.Fatal(err)
	}

	if err := Join(ctx, a, "issues", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	return a
}

// A wait that cannot read the directory exits with the error after a few
// failed rounds instead of passing for a quiet channel, and records it.
func TestWaitExitsWhenTheDirectoryIsLost(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, errors.New("dial tcp: connection refused"))
	fs.fail.Store(true)

	var out bytes.Buffer
	err := Wait(context.Background(), a, nil, time.Minute, &out)
	if err == nil || !strings.Contains(err.Error(), "lost the directory after 5 failed checks") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("a lost directory ends the wait with the error: %v", err)
	}

	w, ok := readWaitState(waitFile(a))
	if !ok || !strings.Contains(w.LastError, "connection refused") || w.LastOkMs != 0 {
		t.Fatalf("wait.json records the failure: %+v", w)
	}
}

// A refusal no retry will fix ends the wait at once and names the identity.
func TestWaitExitsAtOnceOnARefusal(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, fmt.Errorf("%w: HTTP 403 namespace not in your tenant", store.ErrRefused))
	fs.fail.Store(true)

	start := time.Now()
	err := Wait(context.Background(), a, nil, time.Minute, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "refused this identity") || !strings.Contains(err.Error(), a.IdentityPath) {
		t.Fatalf("a 403 ends the wait naming the identity: %v", err)
	}

	if time.Since(start) > 10*WaitPoll {
		t.Fatalf("a refusal must not wait for %d rounds", WaitMaxFailures)
	}
}

// A transient refusal (423, leaderless) is ridden out like any failure.
func TestWaitRidesOutATransientFailure(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, fmt.Errorf("%w: HTTP 423 leaderless", store.ErrRefused))
	fs.fail.Store(true)
	go func() {
		time.Sleep(2 * WaitPoll)
		fs.fail.Store(false)
	}()

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, 20*WaitPoll, &out); err != nil {
		t.Fatalf("recovered before the limit, so no error: %v", err)
	}

	if !strings.Contains(out.String(), "still listening") {
		t.Fatalf("lifetime exit asks to be re-armed: %q", out.String())
	}

	if w, _ := readWaitState(waitFile(a)); w.LastOkMs == 0 || w.LastError != "" {
		t.Fatalf("wait.json shows the recovery: %+v", w)
	}

	if w, _ := readWaitState(waitFile(a)); w.Positions == nil {
		t.Fatalf("wait.json records each conversation's position: %+v", w)
	}
}

// A second wait for the same session replaces the first, which exits
// quietly; while it runs, the session's wait is live and the Stop hook does
// not block.
func TestOneWaitPerSessionAndTheStopHookDefers(t *testing.T) {
	a := followIssues(t)
	withWaitStore(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		err := Wait(ctx, a, nil, 0, &out)
		first <- fmt.Sprint(out.String(), err)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for !WaitLive(a) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	if !WaitLive(a) {
		t.Fatal("a running wait that reached the store is live")
	}

	// With a live wait the Stop hook leaves delivery to the wait.
	o := run(t, a, map[string]any{"hook_event_name": "Stop", "session_id": a.Session})
	if o.Decision == "block" {
		t.Fatalf("a live wait means no Stop block: %+v", o)
	}

	second := make(chan error, 1)
	go func() { second <- Wait(ctx, a, nil, 3*WaitPoll, &bytes.Buffer{}) }()

	select {
	case got := <-first:
		if !strings.Contains(got, "replaced by a newer") {
			t.Fatalf("the first wait says it was replaced: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the first wait did not hand over")
	}

	if err := <-second; err != nil {
		t.Fatalf("the second wait runs normally: %v", err)
	}

	if WaitLive(a) {
		t.Fatal("no wait running, so none is live")
	}
}

// A wait counts as live only while it holds the lock, reached the store
// recently, and covers every conversation the session follows.
func TestWaitLiveNeedsCoverage(t *testing.T) {
	a := followIssues(t)
	if err := os.MkdirAll(waitDir(a), 0o700); err != nil {
		t.Fatal(err)
	}

	lock, err := tryLock(filepath.Join(waitDir(a), "wait.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	state := WaitState{PID: 1, StartedMs: time.Now().UnixMilli(), LastOkMs: time.Now().UnixMilli(), Positions: map[string]int64{"issues": 0}}
	if err := writeJSONFile(waitFile(a), state); err != nil {
		t.Fatal(err)
	}

	if !WaitLive(a) {
		t.Fatal("held, fresh and covering: live")
	}

	var out bytes.Buffer
	if err := CreateShared(context.Background(), a, "other", "", nil, &out); err != nil {
		t.Fatal(err)
	}
	if err := Join(context.Background(), a, "other", "full", "all", "", &out); err != nil {
		t.Fatal(err)
	}

	if WaitLive(a) {
		t.Fatal("a conversation the wait does not cover leaves delivery to the Stop hook")
	}

	state.Positions["other"] = 0
	state.LastOkMs = time.Now().Add(-time.Minute).UnixMilli()
	_ = writeJSONFile(waitFile(a), state)
	if WaitLive(a) {
		t.Fatal("a wait that has not reached the store for a minute is not live")
	}
}
