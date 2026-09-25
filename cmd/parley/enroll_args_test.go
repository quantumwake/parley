package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSplitEnrollArgsEitherOrder(t *testing.T) {
	url := "https://directory.example/enroll/tok"
	flags, pos := splitEnrollArgs([]string{"--default", url})
	if len(pos) != 1 || pos[0] != url || len(flags) != 1 || flags[0] != "--default" {
		t.Fatalf("flag then URL: flags=%v pos=%v", flags, pos)
	}
	flags, pos = splitEnrollArgs([]string{url, "--default"})
	if len(pos) != 1 || pos[0] != url || len(flags) != 1 || flags[0] != "--default" {
		t.Fatalf("URL then flag: flags=%v pos=%v", flags, pos)
	}
}

func TestSplitEnrollArgsURLThatLooksLikeAFlag(t *testing.T) {
	url := "--not-a-real-flag"
	flags, pos := splitEnrollArgs([]string{"--default", url, "--tenant", "acme"})
	if len(pos) != 1 || pos[0] != url {
		t.Fatalf("URL kept: flags=%v pos=%v", flags, pos)
	}
	if len(flags) != 3 || flags[0] != "--default" || flags[1] != "--tenant" || flags[2] != "acme" {
		t.Fatalf("flags: %v", flags)
	}
}

func TestSameSetupRejectsAChangedBinary(t *testing.T) {
	base := setupStamp{Binary: "/usr/local/bin/parley", Version: "0.3.44", CLIs: []string{"grok"}}
	if !sameSetup(base, base) {
		t.Fatal("identical stamps should match")
	}
	moved := base
	moved.Binary = "/opt/parley"
	if sameSetup(base, moved) {
		t.Fatal("a replaced binary must not count as current")
	}
	bumped := base
	bumped.Version = "0.3.45"
	if sameSetup(base, bumped) {
		t.Fatal("a new version must not count as current")
	}
	extra := base
	extra.CLIs = []string{"codex", "grok"}
	if sameSetup(base, extra) {
		t.Fatal("a newly installed CLI must not count as current")
	}
	rewritten := base
	rewritten.Size++
	if sameSetup(base, rewritten) {
		t.Fatal("a larger file at the same path must not count as current")
	}
	// Sub-second noise is the same file. Fly stores 1790341456000000000 for a
	// binary stamped at 1790341456162043006.
	sameSecond := base
	sameSecond.ModTime = 1790341456000000000
	base.ModTime = 1790341456162043006
	if !sameSetup(base, sameSecond) {
		t.Fatal("a second-resolution stat of the same file must count as current")
	}
	nextSecond := base
	nextSecond.ModTime = base.ModTime + int64(time.Second)
	if sameSetup(base, nextSecond) {
		t.Fatal("a newer whole second must re-run setup")
	}
}

func TestStampChangesWhenTheFileIsRewritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "parley")
	if err := os.WriteFile(path, []byte("one"), 0o755); err != nil {
		t.Fatal(err)
	}
	before, err := stampOf(path, "0.3.44", []string{"grok"})
	if err != nil {
		t.Fatal(err)
	}
	// The same bytes, with the file's mtime truncated to the second.
	sec := time.Unix(0, (before.ModTime/int64(time.Second))*int64(time.Second))
	if err := os.Chtimes(path, sec, sec); err != nil {
		t.Fatal(err)
	}
	rounded, err := stampOf(path, "0.3.44", []string{"grok"})
	if err != nil {
		t.Fatal(err)
	}
	if !sameSetup(before, rounded) {
		t.Fatalf("truncating mtime to the second must count as current: %d vs %d", before.ModTime, rounded.ModTime)
	}
	// Same byte length, a later whole second: a rebuild that does not change size.
	later := (before.ModTime/int64(time.Second) + 1) * int64(time.Second)
	if err := os.Chtimes(path, time.Unix(0, later), time.Unix(0, later)); err != nil {
		t.Fatal(err)
	}
	after, err := stampOf(path, "0.3.44", []string{"grok"})
	if err != nil {
		t.Fatal(err)
	}
	if sameSetup(before, after) {
		t.Fatal("a rewritten file at the same path must re-run setup")
	}
	if err := os.WriteFile(path, []byte("one!"), 0o755); err != nil {
		t.Fatal(err)
	}
	grown, err := stampOf(path, "0.3.44", []string{"grok"})
	if err != nil {
		t.Fatal(err)
	}
	if grown.Size == before.Size || sameSetup(before, grown) {
		t.Fatal("a longer file at the same path must re-run setup")
	}
}
