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

// waitBinaryReplaced reports whether the file at path is no longer the
// binary this process started as. A stat that fails, or a new file that
// does not yet answer `version`, is treated as unchanged: a copy in
// progress should not make the wait exec a half-written binary. The next
// round looks again.
func waitBinaryReplaced(path string, started binaryStamp) (string, bool) {
	now, err := stampFile(path)
	if err != nil || now == started {
		return "", false
	}

	ver, err := versionAt(path)
	if err != nil || ver == "" {
		return "", false
	}

	from := strings.TrimSpace(ClientVersion)
	if from == "" {
		from = "unknown"
	}

	return fmt.Sprintf("parley was updated (%s → %s); run `parley wait` again to pick it up", from, ver), true
}

func versionAt(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", err
	}

	line := strings.TrimSpace(string(out))
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = strings.TrimSpace(line[:i])
	}

	fields := strings.Fields(line)
	if len(fields) >= 2 && fields[0] == "parley" {
		return fields[1], nil
	}

	if line == "" {
		return "", fmt.Errorf("no version")
	}

	return line, nil
}

// noteBinaryUpdate prints the update line and answers true when this wait
// should exit so the harness starts it again on the new binary. The
// caller's defer still stops the bells before it releases the poller lock.
func noteBinaryUpdate(w io.Writer, path string, started binaryStamp, ok bool) bool {
	if !ok {
		return false
	}

	msg, changed := waitBinaryReplaced(path, started)
	if !changed {
		return false
	}

	fmt.Fprintln(w, msg)
	return true
}
