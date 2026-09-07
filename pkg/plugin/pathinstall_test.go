package plugin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallPathLinksIntoWritablePathDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ")
	}

	bin := t.TempDir()
	self := filepath.Join(bin, "parley")
	_ = os.WriteFile(self, []byte("#!/bin/sh\necho parley 9.9.9\n"), 0o755)
	target := filepath.Join(t.TempDir(), "userbin")
	_ = os.MkdirAll(target, 0o755)
	t.Setenv("PATH", "/nonexistent-root-dir:"+target)
	env := Env{Self: self}
	link, err := InstallPath(env, "")
	if err != nil {
		t.Fatal(err)
	}

	if filepath.Dir(link) != target {
		t.Fatalf("linked into %s, want %s", link, target)
	}

	if _, ok := OnPath(); !ok {
		t.Fatal("parley must resolve on PATH after linking")
	}

	if msg := EnsurePath(env); msg != "" {
		t.Fatalf("EnsurePath must be silent once on PATH: %q", msg)
	}

	// The PATH entry is a launcher that follows the installed plugin, so a
	// newer build needs no relink and EnsurePath stays silent.
	newer := filepath.Join(bin, "parley-2")
	_ = os.WriteFile(newer, []byte("#!/bin/sh\necho parley 9.9.10\n"), 0o755)
	if msg := EnsurePath(Env{Self: newer}); msg != "" {
		t.Fatalf("launcher in place: EnsurePath must be silent, got %q", msg)
	}

	if !isLauncher(link) {
		t.Fatal("the PATH entry must be the launcher")
	}

	// A stale symlink to an old build is replaced by the launcher.
	_ = os.Remove(link)
	_ = os.Symlink("/tmp/some-old-build", link)
	_ = EnsurePath(Env{Self: newer}) // a dangling link does not resolve on PATH, so this reinstalls
	if !isLauncher(link) {
		t.Fatal("stale symlink must be replaced by the launcher")
	}

	_ = os.Remove(link)
	_ = os.WriteFile(link, []byte("#!/bin/sh\necho someone-elses\n"), 0o755)
	if msg := EnsurePath(Env{Self: self}); msg != "" {
		t.Fatalf("a real file on PATH must be left alone: %q", msg)
	}
}

func TestEnsurePathHintsWhenNothingWritable(t *testing.T) {
	t.Setenv("PATH", "/nonexistent-a:/nonexistent-b")
	msg := EnsurePath(Env{Self: "/tmp/parley"})
	if msg == "" || !contains(msg, "install-path") {
		t.Fatalf("want a hint: %q", msg)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}

	return -1
}
