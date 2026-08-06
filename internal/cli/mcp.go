package cli

import (
	"context"
	"errors"
	"os"
	"runtime"

	"github.com/andyandymike/done-then/internal/mcpserver"
	"github.com/andyandymike/done-then/internal/pluginapi"
	"github.com/andyandymike/done-then/internal/pluginpower"
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
	workspace, err := os.Getwd()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "resolve plugin workspace", err)
	}
	backend := deps.newActionBackend()
	afterStopAvailable := runtime.GOOS == "windows" || runtime.GOOS == "linux"
	afterStopUnavailableReason := "after-stop execute is currently supported only on Windows and systemd Linux"
	options := pluginapi.Options{
		AfterStopExecuteAvailable:         afterStopAvailable,
		AfterStopExecuteUnavailableReason: afterStopUnavailableReason,
		ExecuteUnavailableReason:          "verified-success execute is disabled until Codex provides a verified same-host App Server attachment",
		Workspace:                         workspace,
		Launcher:                          pluginpower.Launcher{DataRoot: root},
		Backend:                           backend,
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
