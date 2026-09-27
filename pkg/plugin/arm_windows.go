//go:build windows

package plugin

// stopSessionWait does not signal a process it cannot prove is parley wait.
// The disarmed record still keeps the next session start quiet.
func stopSessionWait(Env) error { return nil }
