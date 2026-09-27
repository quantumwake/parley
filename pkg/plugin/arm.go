package plugin

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skills/arm/SKILL.md skills/disarm/SKILL.md
var skillFS embed.FS

// The armed file is the record that this session wants a listener. The wait
// exits on a post, a dead directory, or a replaced binary, and that exit
// does not clear the file, so the next session start asks for the wait
// again. disarm writes the other file and removes this one. A disarmed
// session stays quiet even when an old wait.json is still on disk.

func armedPath(env Env) string {
	return filepath.Join(sessionsDir(env), env.Session, "armed")
}

func disarmedPath(env Env) string {
	return filepath.Join(sessionsDir(env), env.Session, "disarmed")
}

func markerSet(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func requireSession(env Env) error {
	if env.Session == "" {
		return errors.New("no session (set PARLEY_SESSION, CLAUDE_CODE_SESSION_ID, GROK_SESSION_ID, or CODEX_THREAD_ID)")
	}

	return nil
}

func writeMarker(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	return os.WriteFile(path, nil, 0o600)
}

func removeMarker(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return err
}

// Arm records that this session wants a listener. It does not start the
// wait: the session has to run that in the background, or the posts land
// where nobody is reading.
func Arm(env Env) error {
	if err := requireSession(env); err != nil {
		return err
	}

	if err := removeMarker(disarmedPath(env)); err != nil {
		return err
	}

	return writeMarker(armedPath(env))
}

// Disarm records that this session stays quiet, and stops its wait.
func Disarm(env Env) error {
	if err := requireSession(env); err != nil {
		return err
	}

	if err := removeMarker(armedPath(env)); err != nil {
		return err
	}

	if err := writeMarker(disarmedPath(env)); err != nil {
		return err
	}

	return stopSessionWait(env)
}

// ArmStatus reports armed, disarmed, or unset, and whether a wait holds
// this session's lock.
func ArmStatus(env Env) (string, bool, error) {
	if err := requireSession(env); err != nil {
		return "", false, err
	}

	state := "unset"
	switch {
	case markerSet(disarmedPath(env)):
		state = "disarmed"
	case markerSet(armedPath(env)):
		state = "armed"
	}

	return state, waitHeld(waitDir(env)), nil
}

// InstallSkills writes the arm and disarm skills under root. A file is
// replaced only when its bytes differ, and only if it is missing or was
// written by parley (the first line of our skill). A person's own skill
// of the same name is left in place.
func InstallSkills(root string) error {
	return fs.WalkDir(skillFS, "skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := skillFS.ReadFile(path)
		if err != nil {
			return err
		}

		name := filepath.Base(filepath.Dir(path))
		dest := filepath.Join(root, name, "SKILL.md")
		if old, err := os.ReadFile(dest); err == nil {
			if string(old) == string(b) {
				return nil
			}

			if !parleySkill(old) {
				return nil
			}
		}

		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}

		return os.WriteFile(dest, b, 0o644)
	})
}

func parleySkill(b []byte) bool {
	return strings.Contains(string(b), "author: parley")
}

// RemoveSkills deletes the arm and disarm skills under root when parley
// wrote them. A person's own skill of the same name is left in place.
func RemoveSkills(root string) error {
	for _, name := range []string{"arm", "disarm"} {
		dest := filepath.Join(root, name, "SKILL.md")
		b, err := os.ReadFile(dest)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if !parleySkill(b) {
			continue
		}
		if err := os.Remove(dest); err != nil {
			return err
		}
		_ = os.Remove(filepath.Dir(dest))
	}
	return nil
}

// RemoveSkillsUnder removes the skills parley wrote for each host.
func RemoveSkillsUnder(home string) error {
	var first error
	for _, rel := range hostSkillDirs {
		if err := RemoveSkills(filepath.Join(home, rel)); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// hostSkillDirs are where Claude, Grok, Codex, and Antigravity look.
// Antigravity's global customization root is ~/.gemini/config, and a skill
// there is config/skills/<name>/SKILL.md. ~/.gemini/skills is not read.
var hostSkillDirs = []string{
	".grok/skills",
	".claude/skills",
	".codex/skills",
	".gemini/config/skills",
}

// InstallSkillsUnder writes the skills under each host directory in home.
func InstallSkillsUnder(home string) error {
	var first error
	for _, rel := range hostSkillDirs {
		if err := InstallSkills(filepath.Join(home, rel)); err != nil && first == nil {
			first = err
		}
	}

	return first
}

// InstallHostSkills writes the skills into this machine's home.
func InstallHostSkills() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	return InstallSkillsUnder(home)
}
