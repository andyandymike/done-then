package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/andyandymike/done-then/internal/supervisor"
	"github.com/andyandymike/done-then/internal/verifierprofile"
)

func verifierCommand(args []string, streams IO, deps dependencies) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		printVerifierUsage(streams.Stdout)
		return 0
	}
	root, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	registry, err := verifierprofile.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize verifier registry", err)
	}
	switch args[0] {
	case "list":
		if len(args) != 1 {
			return usageError(streams.Stderr, errors.New("verifier list accepts no arguments"))
		}
		ids, err := registry.List()
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "list verifier profiles", err)
		}
		if len(ids) == 0 {
			fmt.Fprintln(streams.Stdout, "[DoneThen] No verifier profiles are registered.")
			return 0
		}
		for _, id := range ids {
			profile, loadErr := registry.Load(id)
			if loadErr != nil {
				fmt.Fprintf(streams.Stdout, "%s\tINVALID\n", id)
				continue
			}
			fmt.Fprintf(streams.Stdout, "%s\t%s\t%s\n", profile.ID, profile.Program, shortFingerprint(profile.Fingerprint))
		}
		return 0
	case "add":
		return verifierAdd(args[1:], streams, registry)
	default:
		return usageError(streams.Stderr, errors.New("verifier supports add and list"))
	}
}

func verifierAdd(args []string, streams IO, registry *verifierprofile.Registry) int {
	flags := flag.NewFlagSet("verifier add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "profile id")
	program := flags.String("program", "", "reviewed executable")
	timeout := flags.Duration("timeout", 10*time.Minute, "verifier timeout")
	programHash := flags.String("program-sha256", "", "optional executable SHA-256")
	apply := flags.Bool("apply", false, "write the profile")
	var arguments repeatedStrings
	flags.Var(&arguments, "arg", "fixed verifier argument; repeatable")
	if err := flags.Parse(args); err != nil {
		return usageError(streams.Stderr, err)
	}
	if flags.NArg() != 0 || *id == "" || *program == "" {
		return usageError(streams.Stderr, errors.New("verifier add requires --id and --program"))
	}
	if *timeout < time.Second || *timeout > time.Hour || *timeout%time.Second != 0 {
		return usageError(streams.Stderr, errors.New("verifier timeout must be 1s to 1h in whole seconds"))
	}
	resolved := *program
	if !filepath.IsAbs(resolved) {
		var err error
		resolved, err = exec.LookPath(resolved)
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "resolve verifier executable", err)
		}
	}
	resolved, err := filepath.Abs(resolved)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "resolve verifier executable", err)
	}
	profile := verifierprofile.Profile{
		SchemaVersion: 1, ID: *id, Program: resolved, Args: append([]string(nil), arguments...),
		WorkingDirectory: "armed_workspace", TimeoutSeconds: int64(*timeout / time.Second),
		EnvironmentPolicy: "minimal", ProgramSHA256: *programHash,
	}
	if err := verifierprofile.Validate(profile); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitUsage, "validate verifier profile", err)
	}
	fmt.Fprintf(streams.Stdout, "[DoneThen] Verifier profile plan: id=%s program=%s timeout=%s args=%d\n", profile.ID, profile.Program, *timeout, len(profile.Args))
	if !*apply {
		fmt.Fprintln(streams.Stdout, "[DoneThen] Plan only. Re-run with --apply to install the owner-controlled profile.")
		return 0
	}
	installed, err := registry.Install(profile)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "install verifier profile", err)
	}
	fmt.Fprintf(streams.Stdout, "[DoneThen] Verifier profile %s installed (%s).\n", installed.ID, shortFingerprint(installed.Fingerprint))
	return 0
}
