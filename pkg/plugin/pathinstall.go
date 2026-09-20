package plugin

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// PATH placement. A marketplace install runs no code, so the first hook
// does it: link the binary into a directory that is already on the user's
// PATH and writable without privileges. Nothing is ever written to a root
// owned directory, and nothing is asked in a hook; when no directory
// qualifies the SessionStart context says so and the agent can ask the
// user to add one.

// OnPath reports whether `parley` resolves on the current PATH.
func OnPath() (string, bool) {
	p, err := exec.LookPath(binaryName())
	return p, err == nil
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "parley.exe"
	}

	return "parley"
}

// PathCandidates lists PATH entries the current user may write to, in
// PATH order but with well-known user-level directories first.
func PathCandidates() []string {
	home, _ := os.UserHomeDir()
	preferred := []string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"), "/opt/homebrew/bin", "/usr/local/bin"}
	entries := filepath.SplitList(os.Getenv("PATH"))
	seen := map[string]bool{}
	var out []string
	add := func(d string) {
		d = filepath.Clean(d)
		if d == "" || seen[d] || !onPathList(d, entries) || !writableDir(d) {
			return
		}

		seen[d] = true
		out = append(out, d)
	}
	for _, d := range preferred {
		add(d)
	}

	for _, d := range entries {
		add(d)
	}

	return out
}

func onPathList(d string, entries []string) bool {
	for _, e := range entries {
		if filepath.Clean(e) == d {
			return true
		}
	}

	return false
}

func writableDir(d string) bool {
	st, err := os.Stat(d)
	if err != nil || !st.IsDir() {
		return false
	}

	probe := filepath.Join(d, ".parley-write-probe")
	f, err := os.OpenFile(probe, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false
	}

	f.Close()
	_ = os.Remove(probe)
	return true
}

// isLauncher reports whether the file at p is the current launcher shape.
func isLauncher(p string) bool {
	b, err := os.ReadFile(p)
	return err == nil && strings.Contains(string(b), "parley launcher (written by parley install-path)")
}

// isOurs reports whether a plain file at p is an older parley artifact we
// may replace: a script mentioning parley, never a foreign binary.
func isOurs(p string) bool {
	b, err := os.ReadFile(p)
	return err == nil && len(b) < 4096 && strings.Contains(string(b), "parley")
}

// ErrNoPathDir means no PATH directory is writable by this user.
var ErrNoPathDir = errors.New("no directory on PATH is writable by this user")

// InstallPath links the running binary as `parley` into dir, or into the
// first writable PATH candidate when dir is empty. Returns the link path.
func InstallPath(env Env, dir string) (string, error) {
	if env.Self == "" {
		return "", errors.New("install-path: unknown binary path")
	}

	if dir == "" {
		c := PathCandidates()
		if len(c) == 0 {
			return "", ErrNoPathDir
		}

		dir = c[0]
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	link := filepath.Join(dir, binaryName())
	if runtime.GOOS == "windows" {
		shim := filepath.Join(dir, "parley.cmd")
		return shim, os.WriteFile(shim, []byte(windowsLauncher(env.Self)), 0o755)
	}

	// A launcher, not a symlink to this build: it resolves the plugin
	// currently installed in Claude Code and runs its wrapper, so a
	// `claude plugin update` is picked up on the next command without a
	// session having to fire a hook first. Falls back to this binary.
	_ = os.Remove(link)
	return link, os.WriteFile(link, []byte(unixLauncher(env.Self)), 0o755)
}

func unixLauncher(fallback string) string {
	return `#!/bin/sh
# parley launcher (written by parley install-path): runs the wrapper of the
# plugin version currently installed in Claude Code, so updates apply at once.
reg="$HOME/.claude/plugins/installed_plugins.json"
if [ -f "$reg" ]; then
  root="$(sed -n 's/.*"installPath": *"\([^"]*\/parley\/[^"]*\)".*/\1/p' "$reg" | tail -1)"
  if [ -n "$root" ] && [ -x "$root/scripts/parley" ]; then
    CLAUDE_PLUGIN_DATA="${CLAUDE_PLUGIN_DATA:-$HOME/.claude/plugins/data/parley-parley}" exec sh "$root/scripts/parley" "$@"
  fi
fi
exec "` + fallback + `" "$@"
`
}

func windowsLauncher(fallback string) string {
	return "@echo off\r\n" +
		"for /f \"tokens=*\" %%i in ('powershell -NoProfile -Command \"(Get-Content $env:USERPROFILE\\.claude\\plugins\\installed_plugins.json | ConvertFrom-Json).plugins.'parley@parley'[0].installPath\"') do set ROOT=%%i\r\n" +
		"if exist \"%ROOT%\\scripts\\parley.ps1\" ( powershell -NoProfile -ExecutionPolicy Bypass -File \"%ROOT%\\scripts\\parley.ps1\" %* ) else ( \"" + fallback + "\" %* )\r\n"
}

// EnsurePath is what SessionStart calls: a no-op when parley resolves on
// PATH to this binary; a relink when it resolves to a symlink that points
// elsewhere (an older build); otherwise an automatic link into a writable
// PATH directory; otherwise a hint for the agent to relay. A real file
// named parley on the PATH is never touched.
func EnsurePath(env Env) string {
	if p, ok := OnPath(); ok {
		target, err := os.Readlink(p)
		if err != nil {
			return "" // a real binary someone put there; leave it alone
		}

		if target == env.Self || env.Self == "" {
			return ""
		}

		if _, err := InstallPath(env, filepath.Dir(p)); err == nil {
			return fmt.Sprintf(" `parley` on the PATH (%s) now points at this build.", filepath.Dir(p))
		}

		return ""
	}

	link, err := InstallPath(env, "")
	if err == nil {
		return fmt.Sprintf(" `parley` was linked into %s (on your PATH).", filepath.Dir(link))
	}

	if errors.Is(err, ErrNoPathDir) {
		home, _ := os.UserHomeDir()
		return fmt.Sprintf(" `parley` is not on the PATH and no PATH directory is user-writable; offer the user: `parley install-path --dir <dir>` (for example %s, then add it to PATH), or `export PATH=\"%s/.statefs-ai/bin:$PATH\"` in the shell profile.", filepath.Join(home, ".local", "bin"), strings.TrimRight(home, "/"))
	}

	return " (`parley` could not be linked onto the PATH: " + err.Error() + ")"
}
