package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/codexexec"
	"github.com/andyandymike/done-then/internal/platform"
)

var Version = "0.2.0-dev"

type IO struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type dependencies struct {
	dataRoot         func() (string, error)
	resolveCodex     func(string) (codexexec.Executable, error)
	acquirePowerLock func() (platform.PowerLock, error)
	newActionBackend func() actions.Backend
}

func defaultDependencies() dependencies {
	return dependencies{
		dataRoot:         dataRoot,
		resolveCodex:     codexexec.ResolveExecutable,
		acquirePowerLock: platform.AcquirePowerLock,
		newActionBackend: actions.NewPlatformBackend,
	}
}

func Run(ctx context.Context, args []string, streams IO) int {
	return runWithDependencies(ctx, args, streams, defaultDependencies())
}

func runWithDependencies(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) == 0 {
		printUsage(streams.Stderr)
		return 2
	}
	switch args[0] {
	case "run":
		return runCommand(ctx, args[1:], streams, deps)
	case "mcp":
		return mcpCommand(ctx, args[1:], streams, deps)
	case "hook":
		return hookCommand(args[1:], streams, deps)
	case "supervise":
		return superviseCommand(ctx, args[1:], streams, deps)
	case "cancel-worker":
		return cancelWorkerCommand(ctx, args[1:], streams, deps)
	case "cancel":
		return cancelCommand(ctx, args[1:], streams, deps)
	case "status":
		return statusCommand(args[1:], streams, deps)
	case "reconcile":
		return reconcileCommand(ctx, args[1:], streams, deps)
	case "doctor":
		return doctorCommand(ctx, args[1:], streams, deps)
	case "policy":
		return policyCommand(ctx, args[1:], streams, deps)
	case "verifier":
		return verifierCommand(args[1:], streams, deps)
	case "version", "--version", "-version":
		fmt.Fprintf(streams.Stdout, "donethen %s\n", Version)
		return 0
	case "help", "--help", "-h":
		if len(args) > 1 {
			switch args[1] {
			case "run":
				printRunUsage(streams.Stdout)
			case "cancel":
				printCancelUsage(streams.Stdout)
			case "status":
				printStatusUsage(streams.Stdout)
			case "reconcile":
				printReconcileUsage(streams.Stdout)
			case "doctor":
				printDoctorUsage(streams.Stdout)
			case "policy":
				printPolicyUsage(streams.Stdout)
			case "verifier":
				printVerifierUsage(streams.Stdout)
			case "mcp":
				printMCPUsage(streams.Stdout)
			case "hook":
				printHookUsage(streams.Stdout)
			default:
				printUsage(streams.Stdout)
			}
		} else {
			printUsage(streams.Stdout)
		}
		return 0
	default:
		fmt.Fprintf(streams.Stderr, "[DoneThen] Unknown command %q.\n", args[0])
		printUsage(streams.Stderr)
		return 2
	}
}

func printRunUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen run [options] -- codex exec [options] PROMPT")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Core options:")
	fmt.Fprintln(writer, "  --action shutdown              Required action")
	fmt.Fprintln(writer, "  --dry-run                      Run all gates without a power action (default)")
	fmt.Fprintln(writer, "  --execute                      Allow the configured platform action backend")
	fmt.Fprintln(writer, "  --delay 2m                     Cancellable shutdown delay (30s to 1h)")
	fmt.Fprintln(writer, "  --verify-program PATH          Trusted verifier executable")
	fmt.Fprintln(writer, "  --verify-arg VALUE             Verifier argument; repeat as needed")
	fmt.Fprintln(writer, "  --allow-agent-only-success     Required for execute without a verifier")
	fmt.Fprintln(writer, "  --task-timeout 24h             Codex timeout (1m to 168h)")
	fmt.Fprintln(writer, "  --verify-timeout 10m           Verifier timeout (1s to 1h)")
	fmt.Fprintln(writer, "  --codex-path PATH              Override the Codex executable")
	fmt.Fprintln(writer, "  --keep-artifacts               Keep schema and final-response files")
	fmt.Fprintln(writer, "  --allow-dangerous-codex-flags  Permit restricted Codex flags with a warning")
}

func printCancelUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen cancel [job-id]")
	fmt.Fprintln(writer, "Disarms future action without terminating a running Codex task.")
}

func printStatusUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen status [job-id]")
}

func printReconcileUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen reconcile <job-id>")
	fmt.Fprintln(writer, "Reads platform evidence for a scheduled power job without retrying the action.")
}

func printDoctorUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen doctor [--json]")
	fmt.Fprintln(writer, "Runs read-only capability checks and never schedules or cancels a power action.")
}

func printPolicyUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen policy capture --plugin-id ID [--codex-path PATH] [--allow-agent-only-success] [--apply]")
	fmt.Fprintln(writer, "Captures policy only for experimental verified-success execute. after-stop does not require it.")
}

func printVerifierUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage:")
	fmt.Fprintln(writer, "  donethen verifier list")
	fmt.Fprintln(writer, "  donethen verifier add --id ID --program PATH [--arg VALUE ...] [--timeout 10m] [--program-sha256 HASH] [--apply]")
	fmt.Fprintln(writer, "Without --apply, verifier add validates and prints a plan without writing files.")
}

func printMCPUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen mcp")
	fmt.Fprintln(writer, "Runs the DoneThen stdio MCP server for the Codex plugin.")
}

func printHookUsage(writer io.Writer) {
	fmt.Fprintln(writer, "Usage: donethen hook")
	fmt.Fprintln(writer, "Consumes one Codex hook event from stdin and emits no model-facing output.")
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "DoneThen - cancellable actions when Codex stops")
	fmt.Fprintln(writer)
	fmt.Fprintln(writer, "Usage:")
	fmt.Fprintln(writer, "  donethen run [options] -- codex exec [options] PROMPT")
	fmt.Fprintln(writer, "  donethen cancel [job-id]")
	fmt.Fprintln(writer, "  donethen status [job-id]")
	fmt.Fprintln(writer, "  donethen reconcile <job-id>  Read-only post-boot action reconciliation")
	fmt.Fprintln(writer, "  donethen doctor              Read-only capability and safety diagnostics")
	fmt.Fprintln(writer, "  donethen policy capture ...  Experimental verified-success policy capture")
	fmt.Fprintln(writer, "  donethen verifier ...        Plan, install, or list fixed verifier profiles")
	fmt.Fprintln(writer, "  donethen mcp                 Plugin transport (normally launched by Codex)")
	fmt.Fprintln(writer, "  donethen hook                Plugin observer (normally launched by Codex)")
	fmt.Fprintln(writer, "  donethen version")
}
