package cli

import (
	"errors"
	"fmt"

	"github.com/andyandymike/done-then/internal/hookobserver"
	"github.com/andyandymike/done-then/internal/pluginapi"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

func hookCommand(args []string, streams IO, deps dependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printHookUsage(streams.Stdout)
		return 0
	}
	if len(args) != 0 {
		// Hook handlers must never steer Codex. Report configuration errors only
		// on stderr and still exit successfully.
		fmt.Fprintln(streams.Stderr, "[DoneThen hook] hook accepts no arguments.")
		return 0
	}
	root, err := deps.dataRoot()
	if err != nil {
		fmt.Fprintf(streams.Stderr, "[DoneThen hook] state unavailable: %s.\n", pluginapi.SanitizeError(err))
		return 0
	}
	state, err := pluginstate.New(root)
	if err != nil {
		fmt.Fprintf(streams.Stderr, "[DoneThen hook] state unavailable: %s.\n", pluginapi.SanitizeError(err))
		return 0
	}
	observer, err := hookobserver.New(state)
	if err != nil {
		fmt.Fprintf(streams.Stderr, "[DoneThen hook] observer unavailable: %s.\n", pluginapi.SanitizeError(err))
		return 0
	}
	if err := observer.Handle(streams.Stdin); err != nil && !errors.Is(err, pluginstate.ErrLockTimeout) {
		fmt.Fprintf(streams.Stderr, "[DoneThen hook] event dropped: %s.\n", pluginapi.SanitizeError(err))
	} else if errors.Is(err, pluginstate.ErrLockTimeout) {
		fmt.Fprintln(streams.Stderr, "[DoneThen hook] event dropped: plugin state lock timed out.")
	}
	return 0
}
