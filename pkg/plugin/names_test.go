package plugin

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// A session started yesterday and resumed today lists above today's
// shorter session: the order is last activity, not start.
func TestNamesOrderByLastActivity(t *testing.T) {
	env := Env{DataDir: t.TempDir()}
	resumedAt := time.Now().Add(-time.Minute).Truncate(time.Second)
	NamesPut(env, "agent/2026-09-12T09:00:00/repo#aaaaaaaa", "resumed")
	NamesTouch(env, "agent/2026-09-12T09:00:00/repo#aaaaaaaa", resumedAt)
	NamesPut(env, "agent/2026-09-13T08:00:00/repo#bbbbbbbb", "short")
	NamesTouch(env, "agent/2026-09-13T08:00:00/repo#bbbbbbbb", resumedAt.Add(-time.Hour))

	all := NamesByTime(env)
	if len(all) != 2 || all[0].ID != "resumed" || all[1].ID != "short" {
		t.Fatalf("order: %+v", all)
	}

	if got := ActivityByID(env)["resumed"]; !got.Equal(resumedAt) {
		t.Fatalf("activity: got %v want %v", got, resumedAt)
	}
}

func TestNamesConcurrentPuts(t *testing.T) {
	env := Env{DataDir: t.TempDir()}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			NamesPut(env, fmt.Sprintf("agent/dir-%02d#%08x", i, i), fmt.Sprintf("ns-%d", i))
		}(i)
	}

	wg.Wait()
	all := Names(env)
	if len(all) != 50 {
		t.Fatalf("want 50 names, got %d", len(all))
	}

	if got := namesGet(env, "agent/dir-07#00000007"); got != "ns-7" {
		t.Fatalf("lookup: %q", got)
	}
}
