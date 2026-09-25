package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// withConfig points the config at a temp file, so a test never writes the
// real one.
func withConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("STATEFS_AI_CONFIG", path)
	return path
}

// The rule the owner set: "the default should stand". A machine that has
// said nothing has every optional path off.
func TestEveryFeatureIsOffByDefault(t *testing.T) {
	withConfig(t)
	t.Setenv("PARLEY_FEATURES", "")
	os.Unsetenv("PARLEY_FEATURES")
	env := EnvFromProcess()
	for _, f := range Features {
		if Enabled(env, f.Name) {
			t.Fatalf("%s is on for a machine that never asked for it", f.Name)
		}
	}
}

func TestEnableAndDisableOneAtATime(t *testing.T) {
	withConfig(t)
	os.Unsetenv("PARLEY_FEATURES")

	if changed, err := SetFeature("doorbell", true); err != nil || !changed {
		t.Fatalf("enable: changed=%v err=%v", changed, err)
	}

	env := EnvFromProcess()
	if !Enabled(env, "doorbell") {
		t.Fatal("doorbell did not come on")
	}

	// Mix and match: turning one on says nothing about the others.
	for _, other := range []string{"route-cache", "ticket-cache"} {
		if Enabled(env, other) {
			t.Fatalf("%s came on with the doorbell", other)
		}
	}

	if changed, _ := SetFeature("doorbell", true); changed {
		t.Fatal("enabling an already-on feature reported a change")
	}

	if changed, err := SetFeature("doorbell", false); err != nil || !changed {
		t.Fatalf("disable: changed=%v err=%v", changed, err)
	}

	if Enabled(EnvFromProcess(), "doorbell") {
		t.Fatal("doorbell survived being turned off")
	}
}

// The environment is the whole answer when it is set, so one process can
// differ from the machine — and an empty value turns everything off, which
// is how a test or an operator gets today's behaviour back.
func TestTheEnvironmentDecidesForOneProcess(t *testing.T) {
	withConfig(t)
	if _, err := SetFeature("doorbell", true); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PARLEY_FEATURES", "route-cache")
	env := EnvFromProcess()
	if Enabled(env, "doorbell") {
		t.Fatal("the config's doorbell survived an environment that did not name it")
	}

	if !Enabled(env, "route-cache") {
		t.Fatal("the environment's feature is not on")
	}

	t.Setenv("PARLEY_FEATURES", "")
	if Enabled(EnvFromProcess(), "route-cache") {
		t.Fatal("an empty PARLEY_FEATURES left something on")
	}
}

func TestTheListIsForgivingAboutSeparatorsAndCase(t *testing.T) {
	withConfig(t)
	t.Setenv("PARLEY_FEATURES", " DOORBELL,  route-cache ")
	env := EnvFromProcess()
	for _, want := range []string{"doorbell", "route-cache"} {
		if !Enabled(env, want) {
			t.Fatalf("%s was not read out of the list", want)
		}
	}
}

// A typo must not be written into the config, where it would sit looking
// like something that does nothing.
func TestAnUnknownFeatureIsNotAFeature(t *testing.T) {
	if KnownFeature("doorbel") || KnownFeature("") {
		t.Fatal("a name parley does not have was accepted")
	}

	for _, f := range Features {
		if !KnownFeature(f.Name) {
			t.Fatalf("%s is in the list but not known", f.Name)
		}
	}
}

// The config keeps them sorted and unique, so two enables in either order
// leave the same file.
func TestTheConfigStaysTidy(t *testing.T) {
	withConfig(t)
	os.Unsetenv("PARLEY_FEATURES")
	for _, f := range []string{"ticket-cache", "doorbell", "route-cache"} {
		if _, err := SetFeature(f, true); err != nil {
			t.Fatal(err)
		}
	}

	got := LoadConfig().Features
	if !slices.IsSorted(got) {
		t.Fatalf("features are stored unsorted: %v", got)
	}

	if len(got) != 3 {
		t.Fatalf("features = %v, want three", got)
	}
}

// A config that exists and does not parse must never be written over: the
// enrollment lives in the same file, and LoadConfig answers an empty
// config for anything it cannot read, so a blind save reports success and
// leaves the machine looking unenrolled.
func TestAnUnreadableConfigIsRefusedRatherThanReplaced(t *testing.T) {
	path := withConfig(t)
	if err := os.WriteFile(path, []byte(`{"directory": "https://d.example", "ident`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := SetFeature("doorbell", true); err == nil {
		t.Fatal("a half-written config was saved over without complaint")
	}

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(b), "d.example") {
		t.Fatalf("the unreadable config was overwritten: %q", b)
	}
}

// Concurrent writers leave a config that still reads, with the enrollment
// intact.
//
// HONEST LIMIT, because a test that claims more than it proves is worse
// than none: this runs goroutines in ONE process and it passes with the
// shared-temp-file bug still in place — I tried. The bug was demonstrated
// across PROCESSES (two `parley enable` commands, 20 rounds: 13 lost a
// feature, 7 left unparseable JSON), which is what motivated the unique
// temp name in SaveConfig. This test pins the invariant, not the fix; the
// fix's evidence is that cross-process run.
func TestConcurrentEnablesLeaveAReadableConfig(t *testing.T) {
	path := withConfig(t)
	// A config big enough that one write is several buffers: a small one
	// can slip through a shared temp file by luck, and a test that passes
	// by luck pins nothing. Gates are the real thing that makes a config
	// long.
	seed := Config{Directory: "https://d.example", Identity: "/k/id"}
	for i := range 200 {
		seed.Gates = append(seed.Gates, Gate{Name: fmt.Sprintf("gate-%03d", i), Command: strings.Repeat("x", 64)})
	}

	if err := SaveConfig(seed); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = SetFeature("doorbell", true)
		}()
	}

	wg.Wait()

	c, err := ReadConfig()
	if err != nil {
		t.Fatalf("the config is unreadable after concurrent writers: %v", err)
	}

	if c.Directory != "https://d.example" || c.Identity != "/k/id" || len(c.Gates) != len(seed.Gates) {
		b, _ := os.ReadFile(path)
		t.Fatalf("the config was damaged: directory=%q identity=%q gates=%d (%d bytes on disk)", c.Directory, c.Identity, len(c.Gates), len(b))
	}
}
