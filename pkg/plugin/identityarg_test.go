package plugin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdentityArgResolvesBareNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".statefs", "identities", "swarm-agent-test-1", "identity")
	if got := IdentityArg("swarm-agent-test-1"); got != want {
		t.Fatalf("bare name: got %s want %s", got, want)
	}

	if got := IdentityArg("~/.statefs/identity"); got != "~/.statefs/identity" {
		t.Fatalf("tilde path must pass through: %s", got)
	}

	if got := IdentityArg("/x/y/identity"); got != "/x/y/identity" {
		t.Fatalf("absolute path must pass through: %s", got)
	}

	if got := IdentityArg(""); got != "" {
		t.Fatalf("empty must stay empty: %q", got)
	}

	// A bare name that is an existing file in the working directory is a path.
	dir := t.TempDir()
	f := filepath.Join(dir, "identity")
	_ = os.WriteFile(f, []byte("x"), 0o600)
	cwd, _ := os.Getwd()
	_ = os.Chdir(dir)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	if got := IdentityArg("identity"); got != "identity" {
		t.Fatalf("existing file must pass through: %s", got)
	}
}
