package plugin

import "errors"

// waitExec cannot replace a process on Windows, so a wait there exits on an
// update and asks to be re-armed, as before.
var waitExec = func(string, []string, []string) error {
	return errors.New("exec is not supported on windows")
}
