//go:build soak

package plugin

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestWakeSoak drives the REAL parley binary the way agents do and asks
// whether every post reaches every session. Three sessions of one identity
// each re-arm `parley wait` after a random think time; a chaos loop kills
// random waiters (SIGKILL and SIGTERM); a fourth session posts questions.
// At the end each session must have seen every post, by a wait or by the
// injection a prompt would have made. A post no session-run ever showed is
// a LOSS: the cursor moved past it and it was never displayed.
//
//	PARLEY_SOAK_BIN=/path/to/parley go test -tags soak -run TestWakeSoak -v -count=1 ./pkg/plugin
//
// PARLEY_SOAK_POSTS (default 60), PARLEY_SOAK_SEED (default time) and
// PARLEY_SOAK_CHAOS=0 (no kills) tune it.
func TestWakeSoak(t *testing.T) {
	bin := os.Getenv("PARLEY_SOAK_BIN")
	if bin == "" {
		t.Skip("set PARLEY_SOAK_BIN to the parley binary to soak")
	}

	posts := envInt("PARLEY_SOAK_POSTS", 60)
	seed := int64(envInt("PARLEY_SOAK_SEED", int(time.Now().UnixNano()%1_000_000)))
	rng := rand.New(rand.NewSource(seed))
	var rngMu sync.Mutex
	pick := func(n int) int { rngMu.Lock(); defer rngMu.Unlock(); return rng.Intn(n) }

	ids := []string{"aaaaaaaa-1111-2222-3333-444444444401", "aaaaaaaa-1111-2222-3333-444444444402", "aaaaaaaa-1111-2222-3333-444444444403"}
	writerID := "bbbbbbbb-1111-2222-3333-444444444400"
	envs, _ := sessions(t, append([]string{writerID}, ids...)...)
	writer := envs[writerID]

	ctx := context.Background()
	var sink discard
	if err := CreateShared(ctx, writer, "soak", "", nil, &sink); err != nil {
		t.Fatal(err)
	}

	for _, id := range append([]string{writerID}, ids...) {
		if err := Join(ctx, envs[id], "soak", "full", "all", "", &sink); err != nil {
			t.Fatal(err)
		}
	}

	type run struct {
		mu     sync.Mutex
		proc   *os.Process
		seen   map[int]int // post number -> times printed by a wait
		runs   int
		killed int
		empty  int // waits that ended with no post (lifetime or replaced)
	}

	rs := map[string]*run{}
	for _, id := range ids {
		rs[id] = &run{seen: map[int]int{}}
	}

	re := regexp.MustCompile(`soak-(\d{3})`)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	childEnv := func(id string) []string {
		e := envs[id]
		return append(os.Environ(),
			"STATEFS_KEY_FILE="+e.IdentityPath,
			"STATEFS_AI_DATA="+e.DataDir,
			"CLAUDE_CODE_SESSION_ID="+id,
			"PARLEY_SESSION="+id,
		)
	}

	for _, id := range ids {
		id := id
		r := rs[id]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}

				cmd := exec.Command(bin, "wait")
				cmd.Env = childEnv(id)
				pipe, _ := cmd.StdoutPipe()
				cmd.Stderr = nil
				if err := cmd.Start(); err != nil {
					t.Errorf("start wait: %v", err)
					return
				}

				r.mu.Lock()
				r.proc = cmd.Process
				r.runs++
				r.mu.Unlock()

				buf := make([]byte, 0, 4096)
				chunk := make([]byte, 4096)
				for {
					n, err := pipe.Read(chunk)
					buf = append(buf, chunk[:n]...)
					if err != nil {
						break
					}
				}

				_ = cmd.Wait()
				r.mu.Lock()
				r.proc = nil
				got := 0
				for _, m := range re.FindAllStringSubmatch(string(buf), -1) {
					if n, err := strconv.Atoi(m[1]); err == nil {
						r.seen[n]++
						got++
					}
				}

				if got == 0 {
					r.empty++
				}
				r.mu.Unlock()

				// think time, as an agent handling what it was told
				select {
				case <-stop:
					return
				case <-time.After(time.Duration(pick(1200)) * time.Millisecond):
				}
			}
		}()
	}

	// PARLEY_SOAK_CHAOS=0 turns the kills off, to see what re-arming waits
	// alone does (the everyday case; kills are the pkill and crash case).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(time.Duration(400+pick(900)) * time.Millisecond):
			}

			if os.Getenv("PARLEY_SOAK_CHAOS") == "0" {
				continue
			}

			r := rs[ids[pick(len(ids))]]
			r.mu.Lock()
			if r.proc != nil {
				sig := syscall.SIGKILL
				if pick(10) < 4 {
					sig = syscall.SIGTERM
				}

				_ = r.proc.Signal(sig)
				r.killed++
			}
			r.mu.Unlock()
		}
	}()

	time.Sleep(2 * time.Second) // let the first waiters arm
	for i := 0; i < posts; i++ {
		if err := Post(ctx, writer, "soak", "question", fmt.Sprintf("soak-%03d", i), "", "", nil, &sink); err != nil {
			t.Fatalf("post %d: %v", i, err)
		}

		time.Sleep(time.Duration(pick(350)) * time.Millisecond)
	}

	time.Sleep(8 * time.Second) // let the last posts be delivered
	close(stop)
	for _, id := range ids {
		r := rs[id]
		r.mu.Lock()
		if r.proc != nil {
			_ = r.proc.Kill()
		}
		r.mu.Unlock()
	}

	wg.Wait()

	// A last wait per session, from the binary under test, picks up a wake a
	// killed waiter left; what is still on disk after it is orphaned.
	drained := map[string]string{}
	var drainMu sync.Mutex
	var drainWG sync.WaitGroup
	for _, id := range ids {
		id := id
		drainWG.Add(1)
		go func() {
			defer drainWG.Done()
			out := drainWait(bin, childEnv(id), 6*time.Second)
			drainMu.Lock()
			drained[id] = out
			drainMu.Unlock()
		}()
	}

	drainWG.Wait()

	orphans := 0
	for _, id := range ids {
		for _, f := range []string{wakeFile(envs[id]), wakeFile(envs[id]) + ".taking"} {
			if _, err := os.Stat(f); err == nil {
				orphans++
				t.Logf("ORPHAN left after the final drain: %s", f)
			}
		}
	}

	total, lost, dups := 0, 0, 0
	for _, id := range ids {
		r := rs[id]
		injected := map[int]bool{}
		for _, m := range re.FindAllStringSubmatch(Inject(ctx, envs[id])+drained[id], -1) {
			if n, err := strconv.Atoi(m[1]); err == nil {
				injected[n] = true
			}
		}

		var missing []int
		d := 0
		for i := 0; i < posts; i++ {
			total++
			if r.seen[i] == 0 && !injected[i] {
				missing = append(missing, i)
			}

			if r.seen[i] > 1 {
				d += r.seen[i] - 1
			}
		}

		sort.Ints(missing)
		lost += len(missing)
		dups += d
		t.Logf("session %s: waits=%d killed=%d empty=%d lost=%d duplicates=%d missing=%v", id[len(id)-2:], r.runs, r.killed, r.empty, len(missing), d, missing)
	}

	t.Logf("SOAK RESULT bin=%s seed=%d posts=%d deliveries-expected=%d LOST=%d DUPLICATES=%d ORPHANED-FILES=%d", bin, seed, posts, total, lost, dups, orphans)
	if orphans > 0 {
		t.Fatalf("%d wake files were left behind after the final drain: posts handed to a session and never shown", orphans)
	}

	if lost > 0 {
		t.Fatalf("%d of %d deliveries were lost (the cursor moved, the post was never shown)", lost, total)
	}
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}

	return def
}

// drainWait runs `bin wait` for one session and stops it after d, returning
// what it printed. A wake already waiting is printed at once.
func drainWait(bin string, env []string, d time.Duration) string {
	cmd := exec.Command(bin, "wait")
	cmd.Env = env
	pipe, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		return ""
	}

	timer := time.AfterFunc(d, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	var out []byte
	chunk := make([]byte, 4096)
	for {
		n, err := pipe.Read(chunk)
		out = append(out, chunk[:n]...)
		if err != nil {
			break
		}
	}

	_ = cmd.Wait()
	return string(out)
}

// TestWakeSoakStaleTakingIsRecovered measures the consequence of the window
// the soak cannot hit by chance: a waiter killed after it renamed the wake
// file to wake.taking and before it printed it. That leaves the posts on
// disk with nothing reading them. The binary's next wait must deliver them.
func TestWakeSoakStaleTakingIsRecovered(t *testing.T) {
	bin := os.Getenv("PARLEY_SOAK_BIN")
	if bin == "" {
		t.Skip("set PARLEY_SOAK_BIN to the parley binary to soak")
	}

	id := "aaaaaaaa-1111-2222-3333-444444444401"
	envs, _ := sessions(t, id)
	env := envs[id]
	var sink discard
	if err := CreateShared(context.Background(), env, "soak", "", nil, &sink); err != nil {
		t.Fatal(err)
	}

	if err := Join(context.Background(), env, "soak", "full", "all", "", &sink); err != nil {
		t.Fatal(err)
	}

	taking := wakeFile(env) + ".taking"
	if err := os.MkdirAll(filepath.Dir(taking), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(taking, []byte("soak-999 was mid-take when its waiter was killed\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	childEnv := append(os.Environ(),
		"STATEFS_KEY_FILE="+env.IdentityPath,
		"STATEFS_AI_DATA="+env.DataDir,
		"CLAUDE_CODE_SESSION_ID="+id,
		"PARLEY_SESSION="+id,
	)
	out := drainWait(bin, childEnv, 8*time.Second)
	if !strings.Contains(out, "soak-999") {
		t.Fatalf("a wake left in wake.taking by a waiter killed mid-take was never delivered by the next wait; it is orphaned (output %q)", strings.TrimSpace(out))
	}
}
