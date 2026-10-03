package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/quantumwake/parley/pkg/plugin"
)

// changers are the commands that change something on this machine or the
// directory. Each must answer -h, -help and --help with help and nothing else.
var changers = [][]string{
	{"arm"}, {"disarm"}, {"leave", "c"}, {"join", "c"}, {"grant", "c", "--user", "u"},
	{"enroll", "https://example.test/enroll"}, {"delete", "c"}, {"participant", "x"},
	{"enable", "thinking"}, {"disable", "thinking"}, {"identity", "use", "x"},
	{"uninstall"}, {"setup", "claude"}, {"install-path"}, {"cleanup-conformance"},
	{"conversation", "leave", "c"}, {"wait"}, {"presence"}, {"config", "set", "k", "v"},
}

func TestEveryChangerAnswersHelpOnly(t *testing.T) {
	for _, c := range changers {
		for _, h := range []string{"-h", "-help", "--help"} {
			args := append(append([]string{}, c...), h)
			text, ok := commandHelp(args)
			if !ok {
				t.Fatalf("parley %s is not taken as help", strings.Join(args, " "))
			}

			name := c[0]
			if name == "conversation" {
				name = c[1]
			}

			if !strings.Contains(text, "parley "+name) && !strings.Contains(text, "SETUP") {
				t.Fatalf("parley %s: help does not name the command: %q", strings.Join(args, " "), text)
			}
		}
	}
}

// Text a person wrote can be "--help". It is help only in a flag's place.
func TestHelpInFreeTextIsText(t *testing.T) {
	for _, args := range [][]string{
		{"post", "c", "--text", "--help"},
		{"create", "c", "--description", "-h"},
		{"conversation", "post", "c", "--text", "--help"},
		{"join", "c", "--", "--help"},
		{"status"},
	} {
		if _, ok := commandHelp(args); ok {
			t.Fatalf("parley %s is not help", strings.Join(args, " "))
		}
	}

	for _, args := range [][]string{{"post", "--help"}, {"post", "c", "--text=x", "--help"}, {"find", "-h"}, {"grant", "c", "--user", "--help"}, {"leave", "--all", "--help"}} {
		if _, ok := commandHelp(args); !ok {
			t.Fatalf("parley %s is help", strings.Join(args, " "))
		}
	}
}

func TestHelpLinesAreTheCommandsOwn(t *testing.T) {
	got := helpLines("disarm")
	if !strings.Contains(got, "parley disarm") || strings.Contains(got, "parley arm ") || strings.Contains(got, "parley grant") {
		t.Fatalf("disarm help is disarm's lines only: %q", got)
	}
}

// The binary itself: an armed session runs every changer with --help and
// comes out armed, with nothing on disk changed.
func TestHelpChangesNothing(t *testing.T) {
	if os.Getenv("PARLEY_HELP_MAIN") == "1" {
		os.Args = append([]string{"parley"}, strings.Split(os.Getenv("PARLEY_HELP_ARGS"), "\x1f")...)
		main()
		return
	}

	home := t.TempDir()
	data := filepath.Join(home, "data")
	env := []string{
		"HOME=" + home, "STATEFS_AI_DATA=" + data, "STATEFS_AI_CONFIG=" + filepath.Join(home, "config.json"),
		"STATEFS_AI_STORE=file:" + filepath.Join(home, "store"), "CLAUDE_CODE_SESSION_ID=aaaaaaaa-1111",
		"PARLEY_SESSION=", "PATH=" + os.Getenv("PATH"),
	}
	armed := plugin.Env{DataDir: data, Session: "aaaaaaaa-1111"}
	if err := plugin.Arm(armed); err != nil {
		t.Fatal(err)
	}
	before := tree(t, home)

	for _, c := range changers {
		args := append(append([]string{}, c...), "--help")
		cmd := exec.Command(os.Args[0], "-test.run=^TestHelpChangesNothing$")
		cmd.Env = append(env, "PARLEY_HELP_MAIN=1", "PARLEY_HELP_ARGS="+strings.Join(args, "\x1f"))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("parley %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	if after := tree(t, home); strings.Join(after, "\n") != strings.Join(before, "\n") {
		t.Fatalf("--help changed files:\nbefore %v\nafter  %v", before, after)
	}

	if state, _, _ := plugin.ArmStatus(armed); state != "armed" {
		t.Fatalf("parley disarm --help disarmed the session: %q", state)
	}
}

// tree lists every path under dir with its size.
func tree(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			out = append(out, strings.TrimPrefix(p, dir)+" "+fi.Mode().String()+" "+strconv.FormatInt(fi.Size(), 10))
		}
		return nil
	})
	sort.Strings(out)
	return out
}
