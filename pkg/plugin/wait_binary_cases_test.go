package plugin

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reviewFakeBinary(t *testing.T) (path string, write func(body string)) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "parley")
	write = func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		later := time.Now().Add(3 * time.Second)
		_ = os.Chtimes(path, later, later)
	}
	write("#!/bin/sh\nif [ \"$1\" = version ]; then echo parley 0.3.48; exit 0; fi\n")
	prev := waitExecutable
	waitExecutable = func() (string, error) { return path, nil }
	t.Cleanup(func() { waitExecutable = prev })
	prevVer := ClientVersion
	ClientVersion = "0.3.48"
	t.Cleanup(func() { ClientVersion = prevVer })
	return path, write
}

// A file that is not yet a working binary is left for the next round:
// truncated bytes, a version that exits non-zero, and one that hangs.
func TestAHalfWrittenBinaryDoesNotEndTheWait(t *testing.T) {
	_, write := reviewFakeBinary(t)
	watch := newBinaryWatch()
	if !watch.ok {
		t.Fatal("no stamp for the stand-in")
	}
	cases := map[string]string{
		"truncated":   "\x7fELF\x02\x01\x01\x00\x00\x00", // not executable content
		"exits 1":     "#!/bin/sh\nexit 1\n",
		"hangs":       "#!/bin/sh\nsleep 10\n",
		"stderr only": "#!/bin/sh\necho parley 0.3.49 1>&2; exit 0\n",
	}
	for name, body := range cases {
		write(body + strings.Repeat("#\n", len(name)))
		var out bytes.Buffer
		t0 := time.Now()
		changed := watch.note(&out)
		took := time.Since(t0)
		if changed {
			t.Errorf("%s: the wait would exit on it: %q", name, out.String())
		}
		// The same stamp is not probed twice: a second look is immediate.
		t1 := time.Now()
		if watch.note(&out) || time.Since(t1) > 100*time.Millisecond {
			t.Errorf("%s: the same file was probed again (%s)", name, time.Since(t1))
		}
		if took > 3*time.Second {
			t.Errorf("%s: the probe took %s; the round is stalled", name, took)
		}
		t.Logf("%s: probe took %s", name, took.Round(time.Millisecond))
	}
}

// Something at the path that runs and prints two fields, but is not parley:
// the wait stays up rather than exit onto a file the launcher will refuse.
func TestAForeignBinaryAtThePathDoesNotEndTheWait(t *testing.T) {
	_, write := reviewFakeBinary(t)
	watch := newBinaryWatch()
	write("#!/bin/sh\necho something 9.9.9\n")
	var out bytes.Buffer
	if watch.note(&out) {
		t.Fatalf("a file that is not parley ended the wait: %q", out.String())
	}
}

// An unchanged file never ends a wait, over many rounds.
func TestAnUnchangedBinaryNeverEndsTheWait(t *testing.T) {
	reviewFakeBinary(t)
	a := followIssues(t)
	withBellStore(t, a, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	done := make(chan struct{})
	go func() { _ = Wait(ctx, a, nil, time.Minute, &out); close(done) }()
	select {
	case <-done:
		t.Fatalf("the wait exited with the binary unchanged: %s", out.String())
	case <-time.After(2 * time.Second): // ~100 rounds at 20 ms
	}
	cancel()
	<-done
}

// The herd: every wait on the machine sees the same replaced file and exits
// in the same round; the poller's bells are all stopped before its lock is
// released; the next wait to start takes the lock and opens one set.
func TestEveryWaitExitsTogetherAndOneNewPollerFollows(t *testing.T) {
	_, write := reviewFakeBinary(t)
	s, _ := sessions(t, "aaaaaaaa-1111", "bbbbbbbb-2222", "cccccccc-3333")
	a, b, c := s["aaaaaaaa-1111"], s["bbbbbbbb-2222"], s["cccccccc-3333"]
	follow(t, a, "issues")
	follow(t, b, "issues")
	follow(t, c, "issues")
	bs := withBellStore(t, a, 30*time.Millisecond)
	bs.stopDelay = 100 * time.Millisecond
	t.Setenv("PARLEY_FEATURES", "doorbell")

	ctx, cancel := context.WithCancel(context.Background())
	run := func(env Env) (chan struct{}, *bytes.Buffer) {
		out := &bytes.Buffer{}
		done := make(chan struct{})
		go func() { _ = Wait(ctx, env, nil, time.Minute, out); close(done) }()
		return done, out
	}
	doneA, outA := run(a)
	time.Sleep(200 * time.Millisecond)
	doneB, outB := run(b)
	doneC, outC := run(c)
	time.Sleep(300 * time.Millisecond)
	rungA, _ := bs.counts()
	if rungA == 0 {
		t.Fatal("the poller opened no bells")
	}

	write("#!/bin/sh\nif [ \"$1\" = version ]; then echo parley 0.3.49; exit 0; fi\n# new\n")
	for name, d := range map[string]chan struct{}{"a": doneA, "b": doneB, "c": doneC} {
		select {
		case <-d:
		case <-time.After(3 * time.Second):
			t.Fatalf("wait %s did not exit after the replacement", name)
		}
	}
	for name, o := range map[string]*bytes.Buffer{"a": outA, "b": outB, "c": outC} {
		if !strings.Contains(o.String(), "parley was updated (0.3.48 → 0.3.49)") {
			t.Errorf("wait %s exited without the update line: %q", name, o.String())
		}
	}
	if r, st := bs.counts(); st < rungA || r != rungA {
		t.Fatalf("after the herd exit: rung %d stopped %d", r, st)
	}

	// The new binary's stamp is what the next wait starts from.
	ClientVersion = "0.3.49"
	doneD, _ := run(a)
	deadline := time.Now().Add(3 * time.Second)
	for {
		r, _ := bs.counts()
		if r > rungA {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the new wait did not take the lock and ring")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r, st := bs.counts()
	if open := r - st; open > rungA {
		t.Fatalf("more than one set open after the handover: rung %d stopped %d", r, st)
	}
	select {
	case <-doneD:
		t.Fatal("the new wait exited on its own binary")
	case <-time.After(300 * time.Millisecond):
	}
	cancel()
	<-doneD
}
