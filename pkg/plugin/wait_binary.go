package plugin

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// waitExecutable is the path this process was started from. Tests point it
// at a stand-in so a wait can be shown exiting when that file is replaced.
var waitExecutable = os.Executable

// binaryStamp is the size and the whole-second mtime of one file. The
// second is the resolution #101 settled on: several filesystems, including
// the ones a release is copied onto, do not keep a finer fraction.
type binaryStamp struct {
	size int64
	sec  int64
}

func stampFile(path string) (binaryStamp, error) {
	st, err := os.Stat(path)
	if err != nil {
		return binaryStamp{}, err
	}

	return binaryStamp{size: st.Size(), sec: st.ModTime().Unix()}, nil
}

// binaryWatch is one wait's view of its own executable. probed is the
// stamp last handed to `version`. A hung or foreign file keeps that
// stamp, so later rounds do not run the probe again until the file changes.
type binaryWatch struct {
	path    string
	started binaryStamp
	ok      bool
	probed  binaryStamp
	seen    bool
}

func newBinaryWatch() binaryWatch {
	var w binaryWatch
	path, err := waitExecutable()
	if err != nil {
		return w
	}

	st, err := stampFile(path)
	if err != nil {
		return w
	}

	w.path, w.started, w.ok = path, st, true
	return w
}

// updated answers the version the executable now holds, when it was
// replaced by a parley that answers `version`.
//
// A stat that fails, a file that does not answer `parley version`, or a
// probe of a stamp we already tried, leaves the wait running.
func (b *binaryWatch) updated() (string, bool) {
	if !b.ok {
		return "", false
	}

	now, err := stampFile(b.path)
	if err != nil || now == b.started {
		return "", false
	}

	if b.seen && now == b.probed {
		return "", false
	}

	b.probed, b.seen = now, true
	ver, err := versionAt(b.path)
	if err != nil || ver == "" {
		return "", false
	}

	return ver, true
}

// fromVersion is this process's version for the update line.
func fromVersion() string {
	if from := strings.TrimSpace(ClientVersion); from != "" {
		return from
	}

	return "unknown"
}

// waitUntilEnv carries a wait's deadline across a re-exec, so the wait on
// the new binary ends when the first one would have: a Claude seat's Bash
// timeout counts from the task's start, not from the exec.
const waitUntilEnv = "PARLEY_WAIT_UNTIL_MS"

// reexecWait runs the new binary in this process: same pid, same output,
// so the harness's task goes on and nobody has to re-arm. It answers the
// error when the exec could not happen, and the caller exits as before.
func reexecWait(path string, until time.Time) error {
	env := os.Environ()
	if !until.IsZero() {
		env = append(withoutEnv(env, waitUntilEnv), waitUntilEnv+"="+strconv.FormatInt(until.UnixMilli(), 10))
	}

	return waitExec(path, os.Args, env)
}

func withoutEnv(env []string, name string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, name+"=") {
			out = append(out, kv)
		}
	}

	return out
}

// inheritedLifetime shortens lifetime to the deadline a re-exec carried
// over, and forgets it so a child of this wait does not inherit it.
func inheritedLifetime(lifetime time.Duration, now time.Time) time.Duration {
	v := os.Getenv(waitUntilEnv)
	if v == "" {
		return lifetime
	}

	_ = os.Unsetenv(waitUntilEnv)
	ms, err := strconv.ParseInt(v, 10, 64)
	if err != nil || lifetime <= 0 {
		return lifetime
	}

	left := time.UnixMilli(ms).Sub(now)
	if left < time.Second {
		left = time.Second
	}

	if left < lifetime {
		return left
	}

	return lifetime
}

func versionAt(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "version")
	// Kill grandchildren that keep stdout open after the timeout. Without
	// this, Output waits on their pipe for the child's whole life.
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}

	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}

	fields := strings.Fields(line)
	if len(fields) < 2 || fields[0] != "parley" {
		return "", fmt.Errorf("not a parley version")
	}

	return fields[1], nil
}
