package plugin

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

// A Stop that does not hold the turn keeps what it read for the next prompt,
// and injectLines has already kept its own overflow: each post must still be
// kept once. Kept twice, the file doubled on every such Stop and the same
// batch was delivered again and again (parley development @278).
func TestAStopKeepsEachPostOnceWhenTheBudgetOverflows(t *testing.T) {
	ctx := context.Background()
	a, b := gateEnv(t)
	total := InjectMaxMessages + 5
	for i := 0; i < total; i++ {
		post(t, a, "comment", fmt.Sprintf("respool-post-%03d", i), "")
	}

	stop := func() {
		var out bytes.Buffer
		in := strings.NewReader(`{"hook_event_name":"Stop","session_id":"` + b.Session + `"}`)
		if err := Handle(ctx, b, in, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), `"decision":"block"`) {
			t.Fatalf("an agent's comments do not hold the turn: %q", out.String())
		}
	}
	stop()
	stop()

	seen := map[int]int{}
	for turn := 0; turn < total; turn++ {
		text := Inject(ctx, b)
		if text == "" {
			break
		}
		for i := 0; i < total; i++ {
			seen[i] += strings.Count(text, fmt.Sprintf("respool-post-%03d", i))
		}
	}
	for i := 0; i < total; i++ {
		if seen[i] != 1 {
			t.Fatalf("respool-post-%03d delivered %d times, want once: %v", i, seen[i], seen)
		}
	}
}
