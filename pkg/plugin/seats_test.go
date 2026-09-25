package plugin

import (
	"os"
	"testing"
	"time"
)

// deadPID is a process id that answers nothing. Starting a process and
// reaping it leaves an id that was real and is not, which is the state a
// closed terminal leaves behind.
func deadPID(t *testing.T) int {
	t.Helper()
	p, err := os.StartProcess("/usr/bin/true", []string{"true"}, &os.ProcAttr{})
	if err != nil {
		t.Skipf("cannot start a throwaway process: %v", err)
	}

	if _, err := p.Wait(); err != nil {
		t.Skipf("cannot reap a throwaway process: %v", err)
	}

	return p.Pid
}

// A session that was closed without clearing its presence leaves a file
// saying "listening" forever. The owner it recorded is the only thing on
// disk that can contradict it.
func TestASeatWhoseOwnerIsGoneReadsAsClosed(t *testing.T) {
	_, b := gateEnv(t)
	writePresence(b, presenceFile{State: "listening", AtMs: time.Now().UnixMilli(), OwnerPID: deadPID(t)})

	if got := seatOf(b, b.Session, "someone-else"); !got.Closed {
		t.Fatalf("a seat whose owner is gone does not read as closed: %+v", got)
	}
}

// The seat that is running this code is here by definition, whatever its
// recorded owner looks like - a test binary is not the session that wrote
// the file, and the board must never hide the seat that can act.
func TestMyOwnSeatIsNeverClosed(t *testing.T) {
	_, b := gateEnv(t)
	writePresence(b, presenceFile{State: "working", AtMs: time.Now().UnixMilli(), OwnerPID: deadPID(t)})

	if got := seatOf(b, b.Session, b.Session); got.Closed {
		t.Fatalf("my own seat reads as closed: %+v", got)
	}
}

// A live owner is an open terminal.
func TestASeatWithALiveOwnerIsNotClosed(t *testing.T) {
	_, b := gateEnv(t)
	writePresence(b, presenceFile{State: "listening", AtMs: time.Now().UnixMilli(), OwnerPID: os.Getpid()})

	if got := seatOf(b, b.Session, "someone-else"); got.Closed {
		t.Fatalf("a seat with a live owner reads as closed: %+v", got)
	}
}

// Sessions older than this field recorded no owner. Proving nothing must
// not read as proof the seat is gone.
func TestASeatWithNoRecordedOwnerIsNotClosed(t *testing.T) {
	_, b := gateEnv(t)
	writePresence(b, presenceFile{State: "listening", AtMs: time.Now().UnixMilli()})

	if got := seatOf(b, b.Session, "someone-else"); got.Closed {
		t.Fatalf("a seat with no owner recorded reads as closed: %+v", got)
	}
}
