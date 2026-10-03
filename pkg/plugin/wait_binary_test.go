package plugin

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A running wait exits when the file it was started from is replaced, and
// stays up while that file is unchanged. Removing noteBinaryUpdate from
// the wait loop fails this test: the wait is still running after the
// replacement.
func TestWaitExitsWhenItsBinaryIsReplaced(t *testing.T) {
	clearHosts(t)
	stubExec(t, errors.New("no exec here"))
	dir := t.TempDir()
	path := filepath.Join(dir, "parley")
	write := func(ver string, extra int) {
		t.Helper()
		body := "#!/bin/sh\nif [ \"$1\" = version ]; then echo parley " + ver + "; exit 0; fi\n"
		body += strings.Repeat("# pad\n", extra)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("0.3.48", 0)

	prevPath := waitExecutable
	waitExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { waitExecutable = prevPath })
	prevVer := ClientVersion
	ClientVersion = "0.3.48"
	t.Cleanup(func() { ClientVersion = prevVer })

	a := followIssues(t)
	withBellStore(t, a, 40*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() {
		_ = Wait(ctx, a, nil, time.Minute, &out)
		close(done)
	}()

	select {
	case <-done:
		t.Fatalf("the wait exited before its binary changed: %s", out.String())
	case <-time.After(250 * time.Millisecond):
	}

	write("0.3.49", 4)
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("replacing the binary did not end the wait")
	}

	got := out.String()
	want := "parley was updated (0.3.48 → 0.3.49); run `parley wait` again to pick it up"
	if !strings.Contains(got, want) {
		t.Fatalf("output %q, want the update line", got)
	}
}

func TestVersionAtRejectsAForeignLine(t *testing.T) {
	path := writeStandIn(t, "echo not-parley-at-all")
	if ver, err := versionAt(path); err == nil {
		t.Fatalf("a foreign line was a version: %q", ver)
	}
}

func TestAHungBinaryIsProbedOnce(t *testing.T) {
	path := writeStandIn(t, "sleep 30")
	now, err := stampFile(path)
	if err != nil {
		t.Fatal(err)
	}
	started := now
	started.size++
	b := binaryWatch{path: path, started: started, ok: true}

	start := time.Now()
	if noted(&b) {
		t.Fatal("a hung binary ended the wait")
	}
	first := time.Since(start)
	if first > 4*time.Second {
		t.Fatalf("the probe waited out the child: %s", first)
	}

	start = time.Now()
	if noted(&b) {
		t.Fatal("the same hung file ended the wait on the next round")
	}
	if again := time.Since(start); again > 200*time.Millisecond {
		t.Fatalf("the same stamp was probed again: %s", again)
	}
}

func writeStandIn(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "parley")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// noted is one round of a wait's binary check.
func noted(b *binaryWatch) bool {
	_, ok := b.updated()
	return ok
}

type execCall struct {
	path string
	argv []string
	env  []string
}

// stubExec stands in for the exec, answering err, and records each call.
func stubExec(t *testing.T, err error) *[]execCall {
	t.Helper()
	var calls []execCall
	prev := waitExec
	waitExec = func(path string, argv, env []string) error {
		calls = append(calls, execCall{path, argv, env})
		return err
	}
	t.Cleanup(func() { waitExec = prev })
	return &calls
}

// A wait whose binary is replaced carries on in the same process on the new
// file, with the deadline it started with, and asks nobody to re-arm.
func TestAnUpdatedWaitCarriesOnOnTheNewBinary(t *testing.T) {
	clearHosts(t)
	calls := stubExec(t, nil)
	dir := t.TempDir()
	path := filepath.Join(dir, "parley")
	write := func(ver string, extra int) {
		t.Helper()
		body := "#!/bin/sh\nif [ \"$1\" = version ]; then echo parley " + ver + "; exit 0; fi\n" + strings.Repeat("# pad\n", extra)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("0.3.48", 0)
	prevPath := waitExecutable
	waitExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { waitExecutable = prevPath })

	a := followIssues(t)
	withBellStore(t, a, 40*time.Millisecond)
	started := time.Now()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Wait(context.Background(), a, nil, time.Hour, &out) }()
	time.Sleep(250 * time.Millisecond)

	write("0.3.49", 4)
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the wait did not notice the new binary")
	}

	if len(*calls) != 1 || (*calls)[0].path != path {
		t.Fatalf("the wait execs the new file once: %+v", *calls)
	}
	if strings.Contains(out.String(), "run ") {
		t.Fatalf("nobody is asked to re-arm: %q", out.String())
	}

	var until int64
	for _, kv := range (*calls)[0].env {
		if v, ok := strings.CutPrefix(kv, waitUntilEnv+"="); ok {
			until, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	if end := started.Add(time.Hour).UnixMilli(); until == 0 || until > end+1000 || until < end-5000 {
		t.Fatalf("the deadline is the first wait's, not a fresh hour: until=%d want about %d", until, end)
	}
}

// The wait on the new binary ends when the first one would have.
func TestAReexecutedWaitKeepsItsDeadline(t *testing.T) {
	now := time.Now()
	t.Setenv(waitUntilEnv, strconv.FormatInt(now.Add(20*time.Minute).UnixMilli(), 10))
	if got := inheritedLifetime(110*time.Minute, now); got < 19*time.Minute || got > 20*time.Minute {
		t.Fatalf("lifetime %s, want the 20m left", got)
	}
	if os.Getenv(waitUntilEnv) != "" {
		t.Fatal("the deadline is forgotten once read, so a child does not inherit it")
	}

	t.Setenv(waitUntilEnv, strconv.FormatInt(now.Add(-time.Minute).UnixMilli(), 10))
	if got := inheritedLifetime(110*time.Minute, now); got != time.Second {
		t.Fatalf("a deadline already past leaves a moment to say so: %s", got)
	}

	if got := inheritedLifetime(110*time.Minute, now); got != 110*time.Minute {
		t.Fatalf("no carried deadline keeps the lifetime: %s", got)
	}

	t.Setenv(waitUntilEnv, strconv.FormatInt(now.Add(3*time.Hour).UnixMilli(), 10))
	if got := inheritedLifetime(110*time.Minute, now); got != 110*time.Minute {
		t.Fatalf("a carried deadline never lengthens a wait: %s", got)
	}
}

// The wait itself honours a carried deadline: started with an hour, it ends
// when the deadline its first process set runs out.
func TestAWaitEndsAtTheCarriedDeadline(t *testing.T) {
	clearHosts(t)
	a := followIssues(t)
	t.Setenv(waitUntilEnv, strconv.FormatInt(time.Now().Add(time.Second).UnixMilli(), 10))
	var out bytes.Buffer
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	if err := Wait(ctx, a, nil, time.Hour, &out); err != nil {
		t.Fatalf("the wait ran past its carried deadline: %v", err)
	}
	if took := time.Since(start); took > 5*time.Second || !strings.Contains(out.String(), "still listening") {
		t.Fatalf("the wait ran %s: %q", took, out.String())
	}
}

// At the moment of the exec the wait has let go of everything the new
// process takes again: its session lock, the identity poller lock, and its
// tails. Otherwise the wait on the new binary would find its own lock held,
// and the bells would double for as long as the old tails lingered.
func TestAnUpdatedWaitLetsGoBeforeItExecs(t *testing.T) {
	clearHosts(t)
	t.Setenv("PARLEY_FEATURES", "doorbell")
	dir := t.TempDir()
	path := filepath.Join(dir, "parley")
	write := func(ver string, extra int) {
		t.Helper()
		body := "#!/bin/sh\nif [ \"$1\" = version ]; then echo parley " + ver + "; exit 0; fi\n" + strings.Repeat("# pad\n", extra)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("0.3.48", 0)
	prevPath := waitExecutable
	waitExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { waitExecutable = prevPath })

	a := followIssues(t)
	bs := withBellStore(t, a, 40*time.Millisecond)

	var sessionFree, pollerFree, tailsStopped bool
	prevExec := waitExec
	waitExec = func(string, []string, []string) error {
		if f, err := tryLock(filepath.Join(waitDir(a), "wait.lock")); err == nil {
			sessionFree = true
			f.Close()
		}
		if f, err := tryLock(identityLockPath(a)); err == nil {
			pollerFree = true
			f.Close()
		}
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if rung, stopped := bs.counts(); rung > 0 && stopped == rung {
				tailsStopped = true
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil
	}
	t.Cleanup(func() { waitExec = prevExec })

	done := make(chan error, 1)
	go func() { done <- Wait(context.Background(), a, nil, time.Hour, &bytes.Buffer{}) }()
	waitUntil(t, func() bool { rung, _ := bs.counts(); return rung > 0 }, "the wait opened its tails")

	write("0.3.49", 4)
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not notice the new binary")
	}

	if !sessionFree || !pollerFree || !tailsStopped {
		t.Fatalf("at the exec: session lock free %v, poller lock free %v, tails stopped %v", sessionFree, pollerFree, tailsStopped)
	}
}

// When the exec fails the wait exits as before, and letting go again on the
// way out does not panic.
func TestAFailedExecExitsCleanly(t *testing.T) {
	clearHosts(t)
	t.Setenv("PARLEY_FEATURES", "doorbell")
	stubExec(t, errors.New("no exec here"))
	dir := t.TempDir()
	path := filepath.Join(dir, "parley")
	write := func(ver string, extra int) {
		t.Helper()
		body := "#!/bin/sh\nif [ \"$1\" = version ]; then echo parley " + ver + "; exit 0; fi\n" + strings.Repeat("# pad\n", extra)
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write("0.3.48", 0)
	prevPath := waitExecutable
	waitExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { waitExecutable = prevPath })

	a := followIssues(t)
	bs := withBellStore(t, a, 40*time.Millisecond)
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- Wait(context.Background(), a, nil, time.Hour, &out) }()
	waitUntil(t, func() bool { rung, _ := bs.counts(); return rung > 0 }, "the wait opened its tails")

	write("0.3.49", 4)
	later := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(path, later, later)
	select {
	case err := <-done:
		if err != nil || !strings.Contains(out.String(), "parley was updated") {
			t.Fatalf("err %v out %q", err, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the wait did not notice the new binary")
	}
}
