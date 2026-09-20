package plugin

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quantumwake/statefs/pkg/identityfile"
)

// identityHome points HOME and the config at a temp directory and writes
// three identities: the default, one enrolled by name, and one elsewhere.
func identityHome(t *testing.T) (home string, keys []string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("STATEFS_AI_CONFIG", filepath.Join(home, ".statefs-ai", "config.json"))
	write := func(path, username, directory string) {
		f, err := identityfile.Generate(username)
		if err != nil {
			t.Fatal(err)
		}

		f.Directory = directory
		if err := identityfile.Write(path, f); err != nil {
			t.Fatal(err)
		}

		keys = append(keys, f.PrivateKey)
	}

	write(filepath.Join(home, ".statefs", "identity"), "alice", "")
	write(filepath.Join(home, ".statefs", "identities", "bot", "identity"), "bot-1", "https://dir.example/")
	write(filepath.Join(home, "elsewhere", "manage"), "admin", "")
	if err := os.WriteFile(filepath.Join(home, ".statefs", "identities", "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	return home, keys
}

func TestIdentitiesListsThisMachinesIdentities(t *testing.T) {
	home, keys := identityHome(t)
	bot := filepath.Join(home, ".statefs", "identities", "bot", "identity")
	if err := SaveConfig(Config{Identity: filepath.Join(home, "elsewhere", "manage")}); err != nil {
		t.Fatal(err)
	}

	ids := Identities(bot)
	var got []string
	for _, id := range ids {
		mark := ""
		if id.Current {
			mark = "*"
		}

		got = append(got, id.Name+"="+id.Username+mark)
	}

	want := "default=alice bot=bot-1* " + filepath.Join(home, "elsewhere", "manage") + "=admin"
	if strings.Join(got, " ") != want {
		t.Fatalf("identities:\n got %s\nwant %s", strings.Join(got, " "), want)
	}

	b, _ := json.Marshal(ids)
	for _, k := range keys {
		if strings.Contains(string(b), k) {
			t.Fatal("the listing carries private key material")
		}
	}
}

func TestResolveIdentity(t *testing.T) {
	home, _ := identityHome(t)
	for v, user := range map[string]string{
		"bot":     "bot-1",
		"default": "alice",
		filepath.Join(home, "elsewhere", "manage"): "admin",
		"~/elsewhere/manage":                       "admin",
	} {
		id, err := ResolveIdentity(v)
		if err != nil || id.Username != user {
			t.Fatalf("%s: %+v %v", v, id, err)
		}
	}

	for _, v := range []string{"nope", "", filepath.Join(home, ".statefs", "identities", "notes.txt")} {
		if _, err := ResolveIdentity(v); !errors.Is(err, ErrNoIdentity) {
			t.Fatalf("%q: want ErrNoIdentity, got %v", v, err)
		}
	}
}

// Using an identity writes the config the way enroll --default does: the
// directory follows the identity when its file records one, and the old
// identity's tenant does not carry over.
func TestUseIdentityWritesTheConfig(t *testing.T) {
	identityHome(t)
	if err := SaveConfig(Config{Directory: "https://example.test", Identity: "old", Tenant: "acme"}); err != nil {
		t.Fatal(err)
	}

	bot, _ := ResolveIdentity("bot")
	if _, err := UseIdentity(bot, ""); err != nil {
		t.Fatal(err)
	}

	if c := LoadConfig(); c.Identity != bot.Path || c.Directory != "https://dir.example" || c.Tenant != "" {
		t.Fatalf("config after use bot: %+v", c)
	}

	alice, _ := ResolveIdentity("default")
	if _, err := UseIdentity(alice, "acme"); err != nil {
		t.Fatal(err)
	}

	if c := LoadConfig(); c.Identity != alice.Path || c.Directory != "https://dir.example" || c.Tenant != "acme" {
		t.Fatalf("config after use default: %+v", c)
	}

	t.Setenv("STATEFS_KEY_FILE", "")
	t.Setenv("STATEFS_TENANT", "")
	t.Setenv("STATEFS_DIRECTORY", "")
	if env := EnvFromProcess(); env.IdentityPath != alice.Path || env.Tenant != "acme" {
		t.Fatalf("hooks and commands act as the configured identity: %+v", env)
	}
}

func TestResolveActingSessionThenProject(t *testing.T) {
	identityHome(t)
	t.Setenv("STATEFS_KEY_FILE", "")
	t.Setenv("PARLEY_IDENTITY", "")
	alice, _ := ResolveIdentity("default")
	bot, _ := ResolveIdentity("bot")
	if _, err := UseIdentity(alice, ""); err != nil {
		t.Fatal(err)
	}
	env := EnvFromProcess()
	env.DataDir = t.TempDir()
	if got := env.ResolveActing("", ""); got.IdentityPath != alice.Path {
		t.Fatalf("machine default: %s", got.IdentityPath)
	}
	cwd := t.TempDir()
	if err := PinProjectIdentity(cwd, "bot"); err != nil {
		t.Fatal(err)
	}
	if got := env.ResolveActing("", cwd); got.IdentityPath != bot.Path {
		t.Fatalf("project pin: %s", got.IdentityPath)
	}
	if err := PinSessionIdentity(env, "sess-1", "default"); err != nil {
		t.Fatal(err)
	}
	if got := env.ResolveActing("sess-1", cwd); got.IdentityPath != alice.Path {
		t.Fatalf("session pin wins over project: %s", got.IdentityPath)
	}
}

func TestConfirmIdentityRemoval(t *testing.T) {
	id := LocalIdentity{Name: "bot", Username: "bot-1", Path: "/tmp/x"}
	if err := ConfirmIdentityRemoval(id, true, true, strings.NewReader(""), io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := ConfirmIdentityRemoval(id, true, false, strings.NewReader("nope\n"), io.Discard); err == nil {
		t.Fatal("wrong name must abort")
	}
	if err := ConfirmIdentityRemoval(id, false, false, strings.NewReader("bot-1\n"), io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestRemoveIdentityDeletesFileAndClearsConfig(t *testing.T) {
	home, _ := identityHome(t)
	bot, err := ResolveIdentity("bot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UseIdentity(bot, ""); err != nil {
		t.Fatal(err)
	}
	if err := RemoveIdentity(bot); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bot.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".statefs", "identities", "bot")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("name dir should go when empty")
	}
	if c := LoadConfig(); c.Identity != "" {
		t.Fatalf("config still points at removed identity: %q", c.Identity)
	}
}
