package main

import "strings"

// freeText is the commands whose flags take text a person wrote, where
// "--help" can be the text: `parley post c --text --help`. Their flag sets
// already answer -h in a flag's place without acting.
var freeText = map[string]bool{"post": true, "create": true, "find": true, "describe": true}

// commandHelp answers the help for a command whose arguments ask for it,
// and false when they do not. It runs before any command does, so a
// command that changes something never acts on -h, -help or --help:
// `parley disarm --help` used to disarm the session.
func commandHelp(args []string) (string, bool) {
	if len(args) < 2 {
		return "", false
	}

	cmd, rest := args[0], args[1:]
	if cmd == "conversation" && len(rest) > 0 {
		cmd, rest = rest[0], rest[1:]
	}

	if !asksHelp(cmd, rest) {
		return "", false
	}

	return helpLines(cmd), true
}

func isHelpFlag(a string) bool {
	return a == "-h" || a == "-help" || a == "--help"
}

// asksHelp is true when a help flag stands in a flag's place. Past "--"
// everything is an argument. In a free-text command a help flag right
// after another flag is that flag's value.
func asksHelp(cmd string, args []string) bool {
	for i, a := range args {
		if a == "--" {
			return false
		}

		if !isHelpFlag(a) {
			continue
		}

		if freeText[cmd] && i > 0 && strings.HasPrefix(args[i-1], "-") && !strings.Contains(args[i-1], "=") {
			continue
		}

		return true
	}

	return false
}

// helpLines is the command's entries from the help text: each line that
// starts "  parley <cmd>", with the indented lines under it. A command with
// no entry gets the whole text.
func helpLines(cmd string) string {
	var out []string
	in := false
	for _, line := range strings.Split(usageText(), "\n") {
		trimmed := strings.TrimLeft(line, " ")
		indent := len(line) - len(trimmed)
		switch {
		case indent == 2 && (strings.HasPrefix(trimmed, "parley "+cmd+" ") || trimmed == "parley "+cmd):
			in = true
			out = append(out, line)
		case in && indent > 2 && !strings.HasPrefix(trimmed, "parley "):
			out = append(out, line)
		default:
			in = false
		}
	}

	if len(out) == 0 {
		return usageText()
	}

	return strings.Join(out, "\n") + "\n"
}
