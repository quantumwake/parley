package plugin

// features.go — the switches for paths that are new, optional, and can be
// mixed with each other.
//
// Owner, 2026-09-25: "just make sure all these are feature flag/gated so we
// can mix and match and enable disable them.. the default should stand.
// we'll enable them through parley enable command path maybe?"
//
// So: every new path is OFF until someone turns it on, the default is
// exactly what parley did before it existed, and each one is independent —
// turning the doorbell on says nothing about the route cache. A feature
// nobody has heard of behaves as if it were not there.
//
// Three places can say, in order, so an operator can override a machine and
// a test can override everything:
//
//	PARLEY_FEATURES=doorbell,route-cache   the environment wins
//	~/.statefs-ai/config.json "features"    what `parley enable` writes
//	(nothing)                               the default, which is off

import (
	"os"
	"slices"
	"strings"
)

// Feature is one switchable path. A new one is added here and nowhere
// else, so `parley features` can list them with what they do.
type Feature struct {
	Name string
	What string
}

// Features is every switch parley knows, in the order `parley features`
// prints them.
// A feature is listed only once something READS it: a switch that
// consults nothing makes `parley enable` report success and change
// nothing, which is worse than not offering it.
var Features = []Feature{
	{"doorbell", "wake on a post instead of polling every two seconds (needs a member that serves the live tail)"},
	{"route-cache", "remember which member serves a conversation, so a command does not ask the directory first"},
	{"ticket-cache", "reuse a grant ticket until it expires, instead of minting one per command"},
}

// KnownFeature reports whether name is a feature parley has, so `enable`
// can refuse a typo rather than write it into the config and do nothing.
func KnownFeature(name string) bool {
	return slices.ContainsFunc(Features, func(f Feature) bool { return f.Name == name })
}

// UnknownFeatures answers the names in a PARLEY_FEATURES value that
// parley does not have, so a caller can say so: the variable is the whole
// answer, which makes a typo in it a silent switch-off of everything.
func UnknownFeatures(raw string) []string {
	var out []string
	for _, name := range splitFeatures(raw) {
		if !KnownFeature(name) {
			out = append(out, name)
		}
	}

	return out
}

// Enabled reports whether a feature is on for this process.
//
// PARLEY_FEATURES, when set, is the WHOLE answer: it names every feature
// that is on, so a test or an operator can turn one on without touching
// the config, and can turn everything off with an empty value.
func Enabled(env Env, name string) bool {
	if raw, ok := os.LookupEnv("PARLEY_FEATURES"); ok {
		return slices.Contains(splitFeatures(raw), name)
	}

	return slices.Contains(env.Features, name)
}

// splitFeatures parses a comma or space separated list, ignoring case and
// blanks, so "doorbell, route-cache" and "DOORBELL route-cache" both work.
func splitFeatures(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.ToLower(strings.TrimSpace(f)); f != "" {
			out = append(out, f)
		}
	}

	return out
}

// SetFeature turns one feature on or off in the config and saves it. It
// answers whether anything changed, so `parley enable` can say "already on"
// rather than pretending it did something.
func SetFeature(name string, on bool) (changed bool, err error) {
	// Not LoadConfig: that answers an empty config for a file it could not
	// parse, and saving over one would throw away the directory and the
	// identity while reporting success.
	c, err := ReadConfig()
	if err != nil {
		return false, err
	}

	has := slices.Contains(c.Features, name)
	switch {
	case on && has, !on && !has:
		return false, nil
	case on:
		c.Features = append(c.Features, name)
	default:
		c.Features = slices.DeleteFunc(c.Features, func(f string) bool { return f == name })
	}

	slices.Sort(c.Features)
	return true, SaveConfig(c)
}
