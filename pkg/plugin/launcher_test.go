package plugin

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// launcherRig lays out a plugin root holding the real launcher script, a fake
// parley binary in the plugin data dir, and a home whose ~/.statefs-ai/bin/parley
// is the link to that binary that a first run leaves behind.
func launcherRig(t *testing.T) (root, data, home, linkPath string) {
	t.Helper()
	tmp := t.TempDir()
	root, data, home = filepath.Join(tmp, "root"), filepath.Join(tmp, "data"), filepath.Join(tmp, "home")
	for _, d := range []string{filepath.Join(root, "scripts"), filepath.Join(root, "cmd", "parley"), filepath.Join(data, "bin"), filepath.Join(home, ".statefs-ai", "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	script, err := os.ReadFile("../../scripts/parley")
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "scripts", "parley"), script, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "cmd", "parley", "VERSION"), []byte("0.0.1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	fake := "#!/bin/sh\nif [ \"$1\" = version ]; then echo 'parley 9.9.9'; else echo \"ran $*\"; fi\n"
	if err := os.WriteFile(filepath.Join(data, "bin", "parley"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}

	linkPath = filepath.Join(home, ".statefs-ai", "bin", "parley")
	if err := os.Symlink(filepath.Join(data, "bin", "parley"), linkPath); err != nil {
		t.Fatal(err)
	}

	return root, data, home, linkPath
}

func runLauncher(t *testing.T, root, home string, extra ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", filepath.Join(root, "scripts", "parley"), "mcp")
	cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin"}, extra...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// The MCP server is started without CLAUDE_PLUGIN_DATA, so the launcher looks
// for the binary at ~/.statefs-ai/bin/parley, which is already the link.
// Linking the binary to itself turned it into a loop that broke every session.
func TestLauncherWithoutPluginDataKeepsTheLinkIntact(t *testing.T) {
	root, data, home, link := launcherRig(t)
	want := filepath.Join(data, "bin", "parley")

	out, err := runLauncher(t, root, home)
	if err != nil || out != "ran mcp" {
		t.Fatalf("the launcher must run the binary through the existing link: %q, %v", out, err)
	}

	if got, _ := os.Readlink(link); got != want {
		t.Fatalf("the link must still point at the plugin data binary, got %q want %q", got, want)
	}

	if out, err := exec.Command(link, "version").CombinedOutput(); err != nil || !strings.Contains(string(out), "9.9.9") {
		t.Fatalf("the link must still resolve for everyone else: %q, %v", out, err)
	}
}

func TestLauncherWithPluginDataStillLinksTheBinary(t *testing.T) {
	root, data, home, link := launcherRig(t)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}

	out, err := runLauncher(t, root, home, "CLAUDE_PLUGIN_DATA="+data)
	if err != nil || out != "ran mcp" {
		t.Fatalf("the launcher must run the cached binary: %q, %v", out, err)
	}

	if got, _ := os.Readlink(link); got != filepath.Join(data, "bin", "parley") {
		t.Fatalf("a hook run must (re)create the user link to the data binary, got %q", got)
	}
}

func TestLauncherKeepsANewerRealInstall(t *testing.T) {
	root, data, home, link := launcherRig(t)
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	// curl install leaves a real binary, newer than the plugin VERSION in this rig (0.0.1).
	if err := os.WriteFile(link, []byte("#!/bin/sh\nif [ \"$1\" = version ]; then echo 'parley 9.9.9'; else echo \"install $*\"; fi\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := runLauncher(t, root, home, "CLAUDE_PLUGIN_DATA="+data)
	if err != nil || out != "ran mcp" {
		t.Fatalf("hooks still run the plugin-data binary: %q, %v", out, err)
	}

	if _, err := os.Readlink(link); err == nil {
		t.Fatal("must not replace a newer real install with a symlink to the plugin cache")
	}
	if out, err := exec.Command(link, "version").CombinedOutput(); err != nil || !strings.Contains(string(out), "9.9.9") {
		t.Fatalf("PATH parley must still be the install: %q, %v", out, err)
	}
}
