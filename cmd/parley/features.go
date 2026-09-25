package main

// features.go — `parley features`, `parley enable`, `parley disable`.
//
// Owner, 2026-09-25: "just make sure all these are feature flag/gated so we
// can mix and match and enable disable them.. the default should stand.
// we'll enable them through parley enable command path maybe?"
//
// So the command says three things and no more: what exists, what it does,
// and whether it is on here. Turning one on is one word, turning it off is
// the same word, and a name parley does not know is refused rather than
// written into the config where it would sit doing nothing.

import (
	"fmt"
	"os"
	"strings"

	"github.com/quantumwake/parley/pkg/plugin"
)

func cmdFeatures(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("parley features takes no arguments; use `parley enable <name>` or `parley disable <name>`")
	}

	env := plugin.EnvFromProcess()
	forced, override := os.LookupEnv("PARLEY_FEATURES")

	fmt.Println("Optional paths. Off is the default: parley behaves as it always has until one is turned on.")
	fmt.Println()
	for _, f := range plugin.Features {
		state := "off"
		if plugin.Enabled(env, f.Name) {
			state = "ON"
		}

		fmt.Printf("  %-14s %-3s  %s\n", f.Name, state, f.What)
	}

	fmt.Println()
	if override {
		fmt.Printf("PARLEY_FEATURES=%q is set, so it decides for this process and the config is ignored.\n", forced)
		if unknown := plugin.UnknownFeatures(forced); len(unknown) > 0 {
			// A typo there is a silent switch-off of everything, because
			// the variable is the whole answer. The CLI refuses a typo;
			// the environment cannot, so it is at least named here.
			fmt.Printf("  it names %s, which parley does not have — check the spelling, or everything is off.\n", strings.Join(unknown, ", "))
		}

		return nil
	}

	fmt.Printf("  parley enable <name>    turn one on for this machine (%s)\n", plugin.ConfigPath())
	fmt.Println("  parley disable <name>   turn it off again")
	fmt.Println("  PARLEY_FEATURES=a,b     decide for one process, ignoring the config")
	return nil
}

// cmdEnableFeature is both `enable` and `disable`: the same word, the same
// checks, one boolean apart.
func cmdEnableFeature(args []string, on bool) error {
	verb := "enable"
	if !on {
		verb = "disable"
	}

	if len(args) != 1 {
		return fmt.Errorf("parley %s <name>: one feature at a time (`parley features` lists them)", verb)
	}

	name := strings.ToLower(strings.TrimSpace(args[0]))
	if !plugin.KnownFeature(name) {
		return fmt.Errorf("parley %s: no feature called %q (`parley features` lists them)", verb, name)
	}

	changed, err := plugin.SetFeature(name, on)
	if err != nil {
		return err
	}

	if !changed {
		fmt.Printf("%s was already %s.\n", name, state(on))
		return nil
	}

	fmt.Printf("%s is %s for this machine.\n", name, state(on))
	if _, override := os.LookupEnv("PARLEY_FEATURES"); override {
		fmt.Println("note: PARLEY_FEATURES is set in this shell, and it decides for a process that has it — unset it to see the config take effect.")
	}

	if name == "doorbell" {
		// A running wait started its tails once and keeps doing whatever
		// it was doing until it returns — true on the way in AND out.
		fmt.Println("restart any `parley wait` for it to take effect.")
	}

	return nil
}

func state(on bool) string {
	if on {
		return "on"
	}

	return "off"
}
