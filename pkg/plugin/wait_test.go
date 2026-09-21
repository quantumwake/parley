package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quantumwake/parley/pkg/event"
	"github.com/quantumwake/parley/pkg/store"
)

// failingStore answers Scan with an error for the namespaces set in fail,
// and passes through otherwise. A failure can be limited to a number of
// scans, so a test recovers after N rounds rather than after a sleep.
type failingStore struct {
	store.Store
	mu    sync.Mutex
	fail  map[string]error // namespace id, or "*" for every namespace
	left  int              // scans that still fail when > 0; unlimited when 0
	scans int
}

func (f *failingStore) set(ns string, err error, scans int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail[ns], f.left = err, scans
}

func (f *failingStore) scanCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scans
}

func (f *failingStore) Scan(ctx context.Context, ns string, from, to store.Position) iter.Seq2[event.Event, error] {
	f.mu.Lock()
	f.scans++
	err, ok := f.fail[ns]
	if !ok {
		err, ok = f.fail["*"]
	}

	if ok && f.left > 0 {
		f.left--
		if f.left == 0 {
			f.fail = map[string]error{}
		}
	}
	f.mu.Unlock()

	if ok {
		return func(yield func(event.Event, error) bool) { yield(event.Event{}, err) }
	}

	return f.Store.Scan(ctx, ns, from, to)
}

// withWaitStore makes waits in this test read through one failing store,
// opened once, and shortens the wait's clocks.
func withWaitStore(t *testing.T, env Env) *failingStore {
	t.Helper()
	st, err := StoreFromEnv(env)
	if err != nil {
		t.Fatal(err)
	}

	fs := &failingStore{Store: st, fail: map[string]error{}}
	prev, prevPoll, prevClaim, prevYield := waitStore, WaitPoll, waitClaimTimeout, waitYield
	prevBackoff, prevExit, prevNow := WaitBackoffMax, WaitExitOnUnreachable, waitNow
	waitStore = func(Env) (store.Store, error) { return fs, nil }
	WaitPoll, waitClaimTimeout, waitYield = 20*time.Millisecond, 5*time.Second, 300*time.Millisecond
	WaitBackoffMax, WaitExitOnUnreachable = WaitPoll, false
	t.Cleanup(func() {
		waitStore, WaitPoll, waitClaimTimeout, waitYield = prev, prevPoll, prevClaim, prevYield
		WaitBackoffMax, WaitExitOnUnreachable, waitNow = prevBackoff, prevExit, prevNow
	})
	return fs
}

func follow(t *testing.T, env Env, names ...string) {
	t.Helper()
	var out bytes.Buffer
	for _, name := range names {
		if err := CreateShared(context.Background(), env, name, "", nil, &out); err != nil {
			t.Fatal(err)
		}

		if err := Join(context.Background(), env, name, "full", "all", "", &out); err != nil {
			t.Fatal(err)
		}
	}
}

func followIssues(t *testing.T) Env {
	t.Helper()
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	follow(t, a, "issues")
	return a
}

// A wait that cannot reach the directory (DNS, dial) stays up: that is the
// laptop lid. It records unreachable_since so status can tell armed-offline
// from dead, and it does not pass for a quiet success.
func TestWaitRidesOutALostDirectory(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	fs.set("*", errors.New("dial tcp: lookup directory.statefs.io: no such host"), 0)

	var out bytes.Buffer
	err := Wait(context.Background(), a, nil, 15*WaitPoll, &out)
	if err != nil {
		t.Fatalf("a DNS miss must not end the wait: %v", err)
	}
	if !strings.Contains(out.String(), "still listening") {
		t.Fatalf("lifetime exit asks to be re-armed: %q", out.String())
	}
	if n := fs.scanCount(); n < WaitMaxFailures+2 {
		t.Fatalf("it kept polling past the old 10s limit: %d scans", n)
	}

	w, ok := readWaitState(waitFile(a))
	if !ok || w.UnreachableSinceMs == 0 || !strings.Contains(w.LastError, "no such host") || w.Unreadable["issues"] == "" {
		t.Fatalf("wait.json records armed but offline: %+v", w)
	}
}

// While the wait process is still running and the directory is down, Stop
// and status treat it as armed, not dead.
func TestWaitLiveWhileUnreachable(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	fs.set("*", errors.New("dial tcp: lookup directory.statefs.io: no such host"), 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Wait(ctx, a, nil, 0, io.Discard) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if WaitLive(a) {
			cancel()
			<-done
			return
		}
		time.Sleep(WaitPoll)
	}
	cancel()
	<-done
	t.Fatalf("an offline waiter still holding the lock is live")
}

// --on-unreachable=exit restores the old lost-directory exit.
func TestWaitExitsWhenAskedToLeaveOnUnreachable(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	WaitExitOnUnreachable = true
	fs.set("*", errors.New("dial tcp: connection refused"), 0)

	err := Wait(context.Background(), a, nil, time.Minute, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "lost the directory") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("exit mode still ends the wait with the error: %v", err)
	}
}

// A post made while the directory was unreachable is delivered on the first
// good round; the cursor does not skip it.
func TestWaitDeliversAPostMadeWhileUnreachable(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})

	fs := withWaitStore(t, a)
	fs.set("*", errors.New("dial tcp: lookup directory.statefs.io: no such host"), WaitMaxFailures+8)

	go func() {
		time.Sleep(2 * WaitPoll)
		_ = Post(context.Background(), b, "issues", "question", "posted while down", "", "", nil, &bytes.Buffer{})
	}()

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, time.Minute, &out); err != nil {
		t.Fatalf("recovered and delivered: %v", err)
	}
	if !strings.Contains(out.String(), "posted while down") {
		t.Fatalf("the post made during the outage arrived: %q", out.String())
	}
}

// A wall-clock jump (process was suspended) resets failure counters so a
// resume is not a lost directory.
func TestWaitResumeAfterClockJumpDoesNotExit(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	base := time.Now()
	n := 0
	waitNow = func() time.Time {
		n++
		if n < 4 {
			return base
		}
		return base.Add(2 * time.Minute)
	}
	fs.set("*", errors.New("dial tcp: lookup directory.statefs.io: no such host"), 0)

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, 12*WaitPoll, &out); err != nil {
		t.Fatalf("a clock jump must not end the wait: %v", err)
	}
	if !strings.Contains(out.String(), "still listening") {
		t.Fatalf("lifetime exit asks to be re-armed: %q", out.String())
	}
}

// A refusal no retry will fix ends the wait at once and names the identity;
// the re-armed wait does not report the same refusal again.
func TestWaitExitsAtOnceOnARefusalAndOnlyOnce(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	fs.set("*", fmt.Errorf("%w: HTTP 403 namespace not in your tenant", store.ErrRefused), 0)

	err := Wait(context.Background(), a, nil, time.Minute, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "refused") || !strings.Contains(err.Error(), a.IdentityPath) {
		t.Fatalf("a 403 ends the wait naming the identity: %v", err)
	}

	if n := fs.scanCount(); n > 2 {
		t.Fatalf("a refusal must not wait for %d rounds: %d scans", WaitMaxFailures, n)
	}

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, 10*WaitPoll, &out); err != nil || !strings.Contains(out.String(), "still listening") {
		t.Fatalf("already reported, so the re-armed wait keeps listening: %v %q", err, out.String())
	}
}

// A transient refusal (423, leaderless) is ridden out like any failure.
func TestWaitRidesOutATransientFailure(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	fs.set("*", fmt.Errorf("%w: HTTP 423 leaderless", store.ErrRefused), WaitMaxFailures-2)

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, 20*WaitPoll, &out); err != nil {
		t.Fatalf("recovered before the limit, so no error: %v", err)
	}

	if !strings.Contains(out.String(), "still listening") {
		t.Fatalf("lifetime exit asks to be re-armed: %q", out.String())
	}

	if w, _ := readWaitState(waitFile(a)); w.LastOkMs == 0 || w.LastError != "" || w.Unreadable != nil {
		t.Fatalf("wait.json shows the recovery: %+v", w)
	}
}

// A single 401 (a token the client counted as live after the machine slept)
// reopens the store and is read again; the wait keeps listening.
func TestWaitReauthenticatesOnceOnA401(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	opened := 0
	waitStore = func(Env) (store.Store, error) { opened++; return fs, nil }
	fs.set("*", fmt.Errorf("%w: HTTP 401 authentication required", store.ErrUnauthenticated), 1)

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, 10*WaitPoll, &out); err != nil || !strings.Contains(out.String(), "still listening") {
		t.Fatalf("one 401 is ridden out with a fresh store: %v %q", err, out.String())
	}

	if opened != 2 {
		t.Fatalf("the store is reopened once after the 401: opened %d", opened)
	}
}

// A 401 that comes back after a fresh credential is final, and says it is
// about the credential rather than a grant.
func TestWaitExitsOnA401ThatComesBack(t *testing.T) {
	a := followIssues(t)
	fs := withWaitStore(t, a)
	fs.set("*", fmt.Errorf("%w: HTTP 401 authentication required", store.ErrUnauthenticated), 0)

	err := Wait(context.Background(), a, nil, time.Minute, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "not authenticated") || !strings.Contains(err.Error(), a.IdentityPath) {
		t.Fatalf("a repeated 401 ends the wait naming the credential: %v", err)
	}

	if n := fs.scanCount(); n > 3 {
		t.Fatalf("a repeated 401 must not wait for %d rounds: %d scans", WaitMaxFailures, n)
	}
}

// One unreadable conversation is reported once and does not stop delivery
// from the others.
func TestOneUnreadableConversationDoesNotStopTheOthers(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues", "other")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})

	fs := withWaitStore(t, a)
	fs.set(mustID(t, a, "other"), fmt.Errorf("%w: HTTP 403 no grant", store.ErrRefused), 0)

	err := Wait(context.Background(), a, nil, time.Minute, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot read other") || !strings.Contains(err.Error(), "still followed") {
		t.Fatalf("the revoked conversation is reported on its own: %v", err)
	}

	go func() {
		time.Sleep(5 * WaitPoll)
		_ = Post(context.Background(), b, "issues", "question", "still here", "", "", nil, &bytes.Buffer{})
	}()

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, time.Minute, &out); err != nil || !strings.Contains(out.String(), "still here") {
		t.Fatalf("issues is still delivered and other is not reported again: %v %q", err, out.String())
	}

	if w, _ := readWaitState(waitFile(a)); w.Unreadable["other"] == "" || len(w.Reported) != 1 {
		t.Fatalf("wait.json keeps other unreadable and reported once: %+v", w)
	}
}

// A second wait for the same session replaces that session's waiter, which
// exits quietly; while it runs, the identity poller is live and the Stop
// hook does not block.
func TestOneWaitPerSessionAndTheStopHookDefers(t *testing.T) {
	a := followIssues(t)
	withWaitStore(t, a)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	first := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		err := Wait(ctx, a, nil, 0, &out)
		first <- fmt.Sprint(out.String(), err)
	}()

	waitUntil(t, func() bool { return WaitLive(a) }, "a running wait that reached the store is live")

	if o := run(t, a, map[string]any{"hook_event_name": "Stop", "session_id": a.Session}); o.Decision == "block" {
		t.Fatalf("a live wait means no Stop block: %+v", o)
	}

	second := make(chan error, 1)
	go func() { second <- Wait(ctx, a, nil, 10*WaitPoll, &bytes.Buffer{}) }()

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

	if WaitLive(a) || readClaim(a) != "" {
		t.Fatal("no wait running and no claim left behind")
	}
}

// A claimer that dies before taking the lock does not leave the session
// without a wait: the running wait takes the lock back and carries on.
func TestAClaimerThatDiesDoesNotEndTheWait(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	withWaitStore(t, a)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan string, 1)
	go func() {
		var out bytes.Buffer
		err := Wait(ctx, a, nil, time.Minute, &out)
		done <- fmt.Sprint(out.String(), err)
	}()

	waitUntil(t, func() bool { return WaitLive(a) }, "the wait is live")
	if err := os.WriteFile(claimFile(a), []byte("99999-1"), 0o600); err != nil { // a claimer that was killed
		t.Fatal(err)
	}

	waitUntil(t, func() bool { return readClaim(a) == "" }, "the dead claim is cleared")
	waitUntil(t, func() bool { return WaitLive(a) }, "the wait holds the lock again")
	_ = Post(context.Background(), b, "issues", "question", "after the claim", "", "", nil, &bytes.Buffer{})

	select {
	case got := <-done:
		if !strings.Contains(got, "after the claim") {
			t.Fatalf("the wait kept going and delivered: %q", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the wait did not deliver after a dead claim")
	}
}

// A wait counts as live only while it holds the lock, reached the store
// recently, and covers every conversation the session follows.
func TestWaitLiveNeedsCoverage(t *testing.T) {
	a := followIssues(t)
	if err := os.MkdirAll(waitDir(a), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(identityWaitDir(a), 0o700); err != nil {
		t.Fatal(err)
	}

	lock, err := tryLock(filepath.Join(waitDir(a), "wait.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()

	idLock, err := tryLock(identityLockPath(a))
	if err != nil {
		t.Fatal(err)
	}
	defer idLock.Close()

	state := WaitState{PID: 1, StartedMs: time.Now().UnixMilli(), LastOkMs: time.Now().UnixMilli(), Positions: map[string]int64{"issues": 0}}
	if err := writeJSONFile(waitFile(a), state); err != nil {
		t.Fatal(err)
	}

	if !WaitLive(a) {
		t.Fatal("held, fresh and covering: live")
	}

	follow(t, a, "other")
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

// Two sessions, one identity: one poller lock, two waiters. A post addressed
// to one handle wakes that waiter; the other stays blocked. A wait in B
// does not replace A.
func TestOnePollerTwoWaitersAddressedWake(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	var buf bytes.Buffer
	if err := CreateShared(context.Background(), a, "issues", "", nil, &buf); err != nil {
		t.Fatal(err)
	}

	if err := Join(context.Background(), a, "issues", "full", "all", "grok", &buf); err != nil {
		t.Fatal(err)
	}

	if err := Join(context.Background(), b, "issues", "full", "all", "champion", &buf); err != nil {
		t.Fatal(err)
	}

	fs := withWaitStore(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	gotA := make(chan string, 1)
	gotB := make(chan string, 1)
	go func() {
		defer wg.Done()
		var out bytes.Buffer
		err := Wait(ctx, a, nil, time.Minute, &out)
		gotA <- fmt.Sprint(out.String(), err)
	}()
	go func() {
		defer wg.Done()
		var out bytes.Buffer
		err := Wait(ctx, b, nil, time.Minute, &out)
		gotB <- fmt.Sprint(out.String(), err)
	}()

	waitUntil(t, func() bool { return WaitLive(a) && WaitLive(b) }, "both waiters live under one identity poller")

	if !waitHeld(identityWaitDir(a)) {
		t.Fatal("one identity poller lock is held")
	}

	f, err := tryLock(identityLockPath(a))
	if err == nil {
		f.Close()
		t.Fatal("a second identity lock must not be available")
	}

	n0 := fs.scanCount()
	waitUntil(t, func() bool { return fs.scanCount()-n0 >= 3 }, "identity poller scanned the namespace a few times")
	idle := fs.scanCount() - n0
	if idle > 12 {
		t.Fatalf("idle outbound scans %d; want one Scan per namespace per round, not per session", idle)
	}

	if err := Post(context.Background(), b, "issues", "question", "only grok", "grok", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-gotA:
		if !strings.Contains(got, "only grok") {
			t.Fatalf("grok's waiter prints the addressed post: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("grok's waiter did not wake")
	}

	select {
	case got := <-gotB:
		t.Fatalf("champion's waiter must stay blocked: %q", got)
	case <-time.After(200 * time.Millisecond):
	}

	cancel()
	wg.Wait()
}

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}

		time.Sleep(10 * time.Millisecond)
	}
}

// A wait on some conversations keeps the reported marks of the others, so
// alternating named and full waits does not report the same failure again.
func TestReportedMarksSurviveANamedWait(t *testing.T) {
	a := followIssues(t)
	follow(t, a, "other")
	subs := Subscriptions(a)
	var issuesOnly []Subscription
	for _, s := range subs {
		if s.Name == "issues" {
			issuesOnly = append(issuesOnly, s)
		}
	}

	got := stillFailing([]string{"other"}, issuesOnly, map[string]error{})
	if len(got) != 1 || got[0] != "other" {
		t.Fatalf("a conversation this wait did not read keeps its mark: %v", got)
	}

	if got := stillFailing([]string{"other"}, subs, map[string]error{}); len(got) != 0 {
		t.Fatalf("a conversation read successfully loses its mark: %v", got)
	}
}

// Only a real identity file error counts as a refusal, not any error that
// happens to contain the path.
func TestIdentityFileErrorsAreRefusals(t *testing.T) {
	env := Env{IdentityPath: "id"}
	if refusedForGood(env, errors.New("dial tcp: invalid argument id=3")) {
		t.Fatal("a short path must not match unrelated errors")
	}

	if !refusedForGood(env, &os.PathError{Op: "open", Path: "id", Err: os.ErrNotExist}) {
		t.Fatal("a missing identity file is a refusal")
	}

	if !refusedForGood(env, errors.New("identityfile: bad private key")) {
		t.Fatal("a corrupt identity file is a refusal")
	}
}

// Talk does not wake a listening agent: the wait keeps waiting, and the
// post arrives with the session's next prompt instead. An ask would have
// woken it (TestWaitReturnsOthersPostsNotMine).
func TestTalkDoesNotWakeTheWaitButArrivesAsContext(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})

	if err := Post(context.Background(), b, "issues", "status", "rebuilt the console", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := Wait(context.Background(), a, nil, 2*WaitPoll, &out); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out.String(), "no new posts") {
		t.Fatalf("a status post does not wake the agent: %q", out.String())
	}

	text, hold := InjectHold(context.Background(), a)
	if hold || !strings.Contains(text, "rebuilt the console") {
		t.Fatalf("it arrives with the next prompt, without holding it: hold=%v %q", hold, text)
	}
}

// fanoutRows is the local half of the namespace listener: one Scan's rows
// are filtered per session. Behind-cursor rows, this session's own posts,
// and non-digest kinds in digest mode never become items. The cursor still
// advances to the head of the scan.
func TestFanoutRowsSkipsBehindCursorOwnPostsAndDigest(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111")
	a := s["aaaaaaaa-1111"]
	sub := Subscription{Name: "issues", ID: "ns", Mode: "full", Cursor: 2, Participant: "grok"}
	rows := []nsRow{
		{e: event.Event{Kind: event.KindPostQuestion, SessionID: "bbbbbbbb-2222", To: "grok"}, pos: 1},
		{e: event.Event{Kind: event.KindPostComment, SessionID: "aaaaaaaa-1111", To: "grok"}, pos: 3},
		{e: event.Event{Kind: event.KindPostQuestion, SessionID: "bbbbbbbb-2222", To: "grok"}, pos: 4},
		{e: event.Event{Kind: event.KindPostComment, SessionID: "bbbbbbbb-2222", To: "champion"}, pos: 5},
	}

	items, head := fanoutRows(a, sub, rows, nil)
	if head != 5 {
		t.Fatalf("cursor advances to the scan head, including skipped rows: %d", head)
	}

	if len(items) != 2 {
		t.Fatalf("behind-cursor and own posts are dropped: %+v", items)
	}

	if items[0].pos != 4 || !items[0].mine {
		t.Fatalf("addressed question is kept and marked mine: %+v", items[0])
	}

	if items[1].pos != 5 || items[1].mine {
		t.Fatalf("a post to another handle is kept but not mine: %+v", items[1])
	}

	sub.Mode = "digest"
	sub.Cursor = 0
	digest := []nsRow{
		{e: event.Event{Kind: event.KindPostComment, SessionID: "bbbbbbbb-2222"}, pos: 1},
		{e: event.Event{Kind: event.KindPostReport, SessionID: "bbbbbbbb-2222"}, pos: 2},
	}
	items, head = fanoutRows(a, sub, digest, nil)
	if head != 2 || len(items) != 1 || items[0].e.Kind != event.KindPostReport {
		t.Fatalf("digest mode keeps reports only: head=%d items=%+v", head, items)
	}
}

// scanNamespace is one outbound read. Two posts on one channel are still
// one Scan.
func TestScanNamespaceIsOneOutboundRead(t *testing.T) {
	a := followIssues(t)
	b := a
	b.Session = "bbbbbbbb-2222"
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	fs := withWaitStore(t, a)
	if err := Post(context.Background(), b, "issues", "comment", "one", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	if err := Post(context.Background(), b, "issues", "comment", "two", "", "", nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	n0 := fs.scanCount()
	rows, err := scanNamespace(context.Background(), fs, mustID(t, a, "issues"), 0)
	if err != nil {
		t.Fatal(err)
	}

	if fs.scanCount()-n0 != 1 {
		t.Fatalf("one Scan, got %d", fs.scanCount()-n0)
	}

	if len(rows) < 2 {
		t.Fatalf("both posts come back: %+v", rows)
	}

	for i := 1; i < len(rows); i++ {
		if rows[i].pos != rows[i-1].pos+1 {
			t.Fatalf("positions are consecutive: %+v", rows)
		}
	}
}

// Two sessions following two channels: idle wait is one Scan per namespace
// per round, not one per session.
func TestTwoNamespacesTwoWaitersOneScanEach(t *testing.T) {
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222")
	a, b := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"]
	follow(t, a, "issues", "other")
	_ = Join(context.Background(), b, "issues", "full", "all", "", &bytes.Buffer{})
	_ = Join(context.Background(), b, "other", "full", "all", "", &bytes.Buffer{})
	fs := withWaitStore(t, a)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(2)
	t.Cleanup(func() { cancel(); wg.Wait() })
	go func() { defer wg.Done(); _ = Wait(ctx, a, nil, 0, io.Discard) }()
	go func() { defer wg.Done(); _ = Wait(ctx, b, nil, 0, io.Discard) }()
	waitUntil(t, func() bool { return WaitLive(a) && WaitLive(b) }, "both waiters live")

	// 24 scans on two namespaces are 12 poll rounds if the poller scans each
	// namespace once per round, and about 6 if it scans once per waiter.
	// Rounds are never closer than WaitPoll (each starts a fresh
	// time.After), so a correct poller needs at least 11 intervals and the
	// regression about 5. Timing the 24 scans and failing only when that is
	// too FAST cannot be tripped by a slow runner, unlike the fixed 5-poll
	// window that saw 4 scans on CI at v0.3.27.
	const scans = 24
	n0 := fs.scanCount()
	start := time.Now()
	waitUntil(t, func() bool { return fs.scanCount()-n0 >= scans }, fmt.Sprintf("%d idle scans on two namespaces", scans))
	if took := time.Since(start); took < 10*WaitPoll {
		t.Fatalf("%d idle scans in %s, under 10 polls: a namespace is scanned once per waiter, not once per round", scans, took)
	}
}
