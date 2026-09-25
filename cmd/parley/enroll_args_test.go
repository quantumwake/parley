package main

import "testing"

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
}
