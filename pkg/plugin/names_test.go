package plugin

import (
	"fmt"
	"sync"
	"testing"
)

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
