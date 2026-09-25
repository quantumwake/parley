package plugin

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// note prints the update line and answers true when this wait should exit
// so the harness starts it again on the new binary. The caller's defer
// still stops the bells before it releases the poller lock.
//
// A stat that fails, a file that does not answer `parley version`, or a
// probe of a stamp we already tried, leaves the wait running.
func (b *binaryWatch) note(w io.Writer) bool {
	if !b.ok {
		return false
	}

	now, err := stampFile(b.path)
	if err != nil || now == b.started {
		return false
	}

	if b.seen && now == b.probed {
		return false
	}

	b.probed, b.seen = now, true
	ver, err := versionAt(b.path)
	if err != nil || ver == "" {
		return false
	}

	from := strings.TrimSpace(ClientVersion)
	if from == "" {
		from = "unknown"
	}

	fmt.Fprintf(w, "parley was updated (%s → %s); run `parley wait` again to pick it up\n", from, ver)
	return true
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
