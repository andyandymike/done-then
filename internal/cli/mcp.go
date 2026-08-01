package cli

import (
	"context"
	"errors"

	"github.com/andyandymike/done-then/internal/mcpserver"
	"github.com/andyandymike/done-then/internal/pluginapi"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/supervisor"
)

func mcpCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printMCPUsage(streams.Stdout)
		return 0
	}
	if len(args) != 0 {
		return usageError(streams.Stderr, errors.New("mcp accepts no arguments"))
	}
	root, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	state, err := pluginstate.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize plugin state", err)
	}
	options := pluginapi.Options{
		Backend:                  deps.newActionBackend(),
		ExecuteUnavailableReason: "plugin execute is disabled until Codex provides a verified same-host App Server attachment; policy capture alone cannot enable it",
	}
	service, err := pluginapi.NewWithOptions(state, options)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize plugin service", err)
	}
	server, err := mcpserver.New(service, Version)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize MCP server", err)
	}
	if err := server.Serve(ctx, streams.Stdin, streams.Stdout); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "serve MCP", err)
	}
	return 0
}
