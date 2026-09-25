package main

// participant.go — `parley participant [name]`.
//
// The identity says which machine; the handle says which seat on it is
// talking. Without one, every session on a laptop shows the same name,
// which is what the owner saw on the board: "for the last seat with
// participant name just shows the same identity.. this needs fixing".

import (
	"fmt"

	"github.com/quantumwake/parley/pkg/plugin"
)

func cmdParticipant(args []string) error {
	env := plugin.EnvFromProcess()
	if len(args) == 0 {
		current := plugin.Participant(env)
		if current == "" {
			fmt.Println("no handle in this session, so posts and chips show this machine's identity.")
			fmt.Println("  parley participant <name>    choose one (e.g. champion, reviewer)")
			return nil
		}

		fmt.Printf("%s\n", current)
		return nil
	}

	if len(args) > 1 {
		return fmt.Errorf("parley participant <name>: one handle")
	}

	updated, err := plugin.SetParticipant(env, args[0])
	if err != nil {
		return err
	}

	fmt.Printf("speaking as %s; %d conversation(s) already followed updated, later joins inherit it.\n", args[0], updated)
	if updated > 0 {
		fmt.Println("the chip updates on the next presence ping (a hook, or the next `parley wait`).")
	}

	return nil
}
