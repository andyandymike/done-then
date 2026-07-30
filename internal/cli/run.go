package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/codexexec"
	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/platform"
	"github.com/andyandymike/done-then/internal/store"
	"github.com/andyandymike/done-then/internal/supervisor"
	"github.com/andyandymike/done-then/internal/verifier"
)

type repeatedStrings []string

func (values *repeatedStrings) String() string {
	return strings.Join(*values, ",")
}

func (values *repeatedStrings) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func runCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printRunUsage(streams.Stdout)
		return 0
	}
	doneThenArgs, codexArgs, err := splitAtSeparator(args)
	if err != nil {
		return usageError(streams.Stderr, err)
	}

	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	action := flags.String("action", "", "post-task action")
	execute := flags.Bool("execute", false, "allow a real action")
	dryRunFlag := flags.Bool("dry-run", false, "force dry-run mode")
	delay := flags.Duration("delay", 2*time.Minute, "shutdown countdown")
	taskTimeout := flags.Duration("task-timeout", 24*time.Hour, "Codex timeout")
	allowAgentOnly := flags.Bool("allow-agent-only-success", false, "allow completion without an external verifier")
	verifyProgram := flags.String("verify-program", "", "external verifier executable")
	var verifyArgs repeatedStrings
	flags.Var(&verifyArgs, "verify-arg", "external verifier argument (repeatable)")
	verifyTimeout := flags.Duration("verify-timeout", 10*time.Minute, "external verifier timeout")
	codexPath := flags.String("codex-path", "", "Codex executable path")
	allowDangerous := flags.Bool("allow-dangerous-codex-flags", false, "allow restricted Codex flags")
	keepArtifacts := flags.Bool("keep-artifacts", false, "retain completion artifacts")
	if err := flags.Parse(doneThenArgs); err != nil {
		return usageError(streams.Stderr, err)
	}
	if flags.NArg() != 0 {
		return usageError(streams.Stderr, fmt.Errorf("unexpected DoneThen argument %q", flags.Arg(0)))
	}
	if *action != "shutdown" {
		return usageError(streams.Stderr, errors.New("--action shutdown is required in v0.1"))
	}
	if *execute && *dryRunFlag {
		return usageError(streams.Stderr, errors.New("--execute and --dry-run are mutually exclusive"))
	}
	dryRun := !*execute
	if *delay < 30*time.Second || *delay > time.Hour {
		return usageError(streams.Stderr, errors.New("--delay must be between 30s and 1h"))
	}
	if *delay%time.Second != 0 {
		return usageError(streams.Stderr, errors.New("--delay must use whole seconds"))
	}
	if *taskTimeout < time.Minute || *taskTimeout > 168*time.Hour {
		return usageError(streams.Stderr, errors.New("--task-timeout must be between 1m and 168h"))
	}
	if *verifyTimeout < time.Second || *verifyTimeout > time.Hour {
		return usageError(streams.Stderr, errors.New("--verify-timeout must be between 1s and 1h"))
	}
	if len(verifyArgs) != 0 && *verifyProgram == "" {
		return usageError(streams.Stderr, errors.New("--verify-arg requires --verify-program"))
	}
	if *execute && *verifyProgram == "" && !*allowAgentOnly {
		return usageError(streams.Stderr, errors.New("execute mode without a verifier requires --allow-agent-only-success"))
	}

	currentDir, err := os.Getwd()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate current directory", err)
	}
	invocation, err := codexexec.ParseInvocation(codexArgs, currentDir, *allowDangerous)
	if err != nil {
		return usageError(streams.Stderr, err)
	}
	userPrompt := invocation.Prompt
	if invocation.PromptFromStdin {
		userPrompt, err = readBounded(streams.Stdin, codexexec.MaxPromptBytes)
		if err != nil {
			return usageError(streams.Stderr, fmt.Errorf("read prompt from stdin: %w", err))
		}
		if userPrompt == "" {
			return usageError(streams.Stderr, errors.New("stdin prompt must not be empty"))
		}
	}
	combinedPrompt := codexexec.ComposePrompt(userPrompt)

	dataRoot, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	jobStore, err := store.New(dataRoot)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize DoneThen state", err)
	}

	var powerLock platform.PowerLock
	if *execute {
		powerLock, err = deps.acquirePowerLock()
		if err != nil {
			if errors.Is(err, platform.ErrPowerLockHeld) {
				fmt.Fprintln(streams.Stderr, "[DoneThen] Shutdown not scheduled: another power job is active.")
				return supervisor.ExitActiveJobConflict
			}
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "acquire power-job lock", err)
		}
		defer powerLock.Release()
		if err := jobStore.RecoverPreActionJobs(); err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "recover old jobs", err)
		}
		active, err := jobStore.ActivePowerJobs()
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "inspect active power jobs", err)
		}
		if len(active) != 0 {
			fmt.Fprintf(streams.Stderr, "[DoneThen] Shutdown not scheduled: unresolved power job %s is in state %s.\n", active[0].JobID, active[0].State)
			fmt.Fprintf(streams.Stderr, "[DoneThen] Resolve it with: donethen cancel %s\n", active[0].JobID)
			return supervisor.ExitActiveJobConflict
		}
	}

	jobIdentity, err := identity.New()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "create job identity", err)
	}
	artifactDir, err := jobStore.ArtifactDir(jobIdentity.JobID)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "create job artifacts", err)
	}
	executable, err := deps.resolveCodex(*codexPath)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitAgentFailed, "resolve Codex", err)
	}
	if len(invocation.DangerousFlags) != 0 {
		fmt.Fprintln(streams.Stderr, "[DoneThen] WARNING: dangerous Codex flags are enabled.")
		fmt.Fprintln(streams.Stderr, "[DoneThen] Codex may be able to invoke host power commands outside DoneThen's safety boundary.")
	}

	agent := codexexec.Runner{
		Executable:     executable,
		Invocation:     invocation,
		CombinedPrompt: combinedPrompt,
		ArtifactDir:    artifactDir,
		TaskTimeout:    *taskTimeout,
		KeepArtifacts:  *keepArtifacts,
		Stdout:         streams.Stdout,
		Stderr:         streams.Stderr,
	}
	var verifyRunner supervisor.Verifier
	if *verifyProgram != "" {
		verifyRunner = &verifier.Runner{
			Program: *verifyProgram,
			Args:    append([]string(nil), verifyArgs...),
			Dir:     invocation.WorkingDir,
			Timeout: *verifyTimeout,
			Stdout:  streams.Stdout,
			Stderr:  streams.Stderr,
		}
	}
	var backend actions.Backend
	if *execute {
		backend = deps.newActionBackend()
	}
	coordinator, err := supervisor.New(supervisor.Config{
		JobID:        jobIdentity.JobID,
		NonceHash:    jobIdentity.NonceHash,
		DryRun:       dryRun,
		Action:       *action,
		Delay:        *delay,
		CodexCWD:     invocation.WorkingDir,
		PromptSHA256: identity.SHA256([]byte(combinedPrompt)),
		Agent:        agent,
		Verifier:     verifyRunner,
		Backend:      backend,
		Store:        jobStore,
	})
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "configure supervisor", err)
	}

	mode := "dry-run"
	if *execute {
		mode = "execute"
	}
	fmt.Fprintf(streams.Stderr, "[DoneThen] Armed job %s (mode=%s, action=shutdown, delay=%s).\n", jobIdentity.JobID, mode, delay.String())
	outcome := coordinator.Run(ctx)
	if outcome.ExitCode == supervisor.ExitOK {
		if outcome.State == supervisor.StateDryRunComplete {
			fmt.Fprintf(streams.Stderr, "[DoneThen] Dry-run complete for %s: all gates passed; no action was executed.\n", outcome.JobID)
		} else {
			fmt.Fprintf(streams.Stderr, "[DoneThen] %s\n", outcome.Reason)
			fmt.Fprintf(streams.Stderr, "[DoneThen] Cancel with: donethen cancel %s\n", outcome.JobID)
		}
		return 0
	}
	if outcome.ActionMayBeScheduled {
		fmt.Fprintf(streams.Stderr, "[DoneThen] WARNING: a shutdown may already be scheduled for job %s.\n", outcome.JobID)
		fmt.Fprintf(streams.Stderr, "[DoneThen] Run immediately: donethen cancel %s\n", outcome.JobID)
		fmt.Fprintf(streams.Stderr, "[DoneThen] Job failed after the action boundary: %s.\n", outcome.Reason)
	} else {
		fmt.Fprintf(streams.Stderr, "[DoneThen] Shutdown not scheduled: %s.\n", outcome.Reason)
	}
	if outcome.JobID != "" {
		fmt.Fprintf(streams.Stderr, "[DoneThen] Job: %s (state=%s)\n", outcome.JobID, outcome.State)
	}
	return outcome.ExitCode
}

func splitAtSeparator(args []string) ([]string, []string, error) {
	for index, argument := range args {
		if argument == "--" {
			if index == len(args)-1 {
				return nil, nil, errors.New("missing codex exec command after --")
			}
			return args[:index], args[index+1:], nil
		}
	}
	return nil, nil, errors.New("run requires -- before codex exec")
}

func readBounded(reader io.Reader, maximum int64) (string, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maximum+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maximum {
		return "", fmt.Errorf("input exceeds %d bytes", maximum)
	}
	return string(data), nil
}

func dataRoot() (string, error) {
	return store.DefaultRoot()
}

func usageError(writer io.Writer, err error) int {
	fmt.Fprintf(writer, "[DoneThen] %v.\n", err)
	fmt.Fprintln(writer, "[DoneThen] Run 'donethen help' for usage.")
	return supervisor.ExitUsage
}

func runtimeError(writer io.Writer, exitCode int, operation string, err error) int {
	fmt.Fprintf(writer, "[DoneThen] %s: %v.\n", operation, err)
	return exitCode
}
