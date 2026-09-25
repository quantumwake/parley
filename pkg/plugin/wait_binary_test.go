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

// A running wait exits when the file it was started from is replaced, and
// stays up while that file is unchanged. Removing noteBinaryUpdate from
// the wait loop fails this test: the wait is still running after the
// replacement.
func TestWaitExitsWhenItsBinaryIsReplaced(t *testing.T) {
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
	if b.note(&bytes.Buffer{}) {
		t.Fatal("a hung binary ended the wait")
	}
	first := time.Since(start)
	if first > 4*time.Second {
		t.Fatalf("the probe waited out the child: %s", first)
	}

	start = time.Now()
	if b.note(&bytes.Buffer{}) {
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
