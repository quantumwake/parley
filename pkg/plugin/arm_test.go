package plugin

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisarmKeepsARestartedSessionQuiet(t *testing.T) {
	_, b := gateEnv(t)
	started := timeNowForTest()
	if err := writeJSONFile(waitFile(b), WaitState{PID: 1<<30 - 1, StartedMs: started}); err != nil {
		t.Fatal(err)
	}

	if err := Disarm(b); err != nil {
		t.Fatal(err)
	}

	if got := sessionStart(context.Background(), b, false); strings.Contains(got, "had a listener armed") {
		t.Fatalf("disarm silenced the restart: %q", got)
	}

	state, _, err := ArmStatus(b)
	if err != nil || state != "disarmed" {
		t.Fatalf("status: %q %v", state, err)
	}
}

func TestArmAsksEvenWhenNoWaitFileRemains(t *testing.T) {
	_, b := gateEnv(t)
	if err := Arm(b); err != nil {
		t.Fatal(err)
	}

	got := sessionStart(context.Background(), b, false)
	if !strings.Contains(got, "had a listener armed before it restarted") {
		t.Fatalf("an armed session with no wait.json is still told: %q", got)
	}
	if !strings.Contains(got, "last started earlier") {
		t.Fatalf("and does not invent a time: %q", got)
	}
}

func TestRemoveSkillsLeavesAForeignFile(t *testing.T) {
	root := t.TempDir()
	if err := InstallSkills(root); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(root, "arm", "SKILL.md")
	if err := os.WriteFile(foreign, []byte("---\nname: arm\ndescription: mine\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSkills(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Fatal("a person's arm skill was removed")
	}
	if _, err := os.Stat(filepath.Join(root, "disarm", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("the parley disarm skill was left installed")
	}
}

func TestInstallSkillsLeavesAForeignFile(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "arm", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("---\nname: arm\ndescription: mine\n---\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := InstallSkills(root); err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "author: parley") {
		t.Fatal("a person's arm skill was replaced")
	}

	disarm := filepath.Join(root, "disarm", "SKILL.md")
	if _, err := os.Stat(disarm); err != nil {
		t.Fatal("the missing disarm skill was not installed")
	}
}

func TestInstallSkillsUnderWritesEachHost(t *testing.T) {
	home := t.TempDir()
	if err := InstallSkillsUnder(home); err != nil {
		t.Fatal(err)
	}

	for _, rel := range hostSkillDirs {
		b, err := os.ReadFile(filepath.Join(home, rel, "arm", "SKILL.md"))
		if err != nil {
			t.Fatal(rel, err)
		}

		if !strings.Contains(string(b), "tracked background task") || !strings.Contains(string(b), "Do not append `&`") {
			t.Fatal(rel, "skill does not say how to start the wait")
		}
	}
}

func timeNowForTest() int64 { return 1_700_000_000_000 }
