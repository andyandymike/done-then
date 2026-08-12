package cli

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
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
	backendSupported := runtime.GOOS == "windows" || runtime.GOOS == "linux"
	backendPreflightPassed := false
	if backendSupported {
		probe := actions.PowerRequest{
			JobID: "dt_mcp_capability_probe", Action: "shutdown", Delay: 2 * time.Minute,
			Comment: "DoneThen capability probe", RequestedAt: time.Now().UTC(),
		}
		capabilities, preflightErr := backend.Preflight(ctx, probe)
		backendPreflightPassed = preflightErr == nil && capabilities.ExecuteSupported
	}
	stopUnavailableReason := "stop_arbitration_unavailable: Codex Stop observation is not a final global hook-arbitration receipt; Stop-based execute remains disabled"
	verifiedUnavailableReason := "verified_success_authority_unavailable: verified-success execute requires validated local policy and a trusted same-host authority attachment"
	options := pluginapi.Options{
		PolicyCapabilities: map[pluginstate.TriggerPolicy]pluginapi.PolicyCapability{
			pluginstate.TriggerAfterStop: {
				BuildSupported: true, BackendSupported: backendSupported,
				BackendPreflightPassed: backendPreflightPassed, ExecuteReady: false,
				UnavailableReason: stopUnavailableReason,
			},
			pluginstate.TriggerAfterAllStop: {
				BuildSupported: true, BackendSupported: backendSupported,
				BackendPreflightPassed: backendPreflightPassed, ExecuteReady: false,
				UnavailableReason: stopUnavailableReason,
			},
			pluginstate.TriggerVerifiedSuccess: {
				BuildSupported: true, BackendSupported: backendSupported,
				BackendPreflightPassed: backendPreflightPassed, ExecuteReady: false,
				UnavailableReason: verifiedUnavailableReason,
			},
		},
		Workspace: workspace,
		Launcher:  pluginpower.Launcher{DataRoot: root},
		Backend:   backend,
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
