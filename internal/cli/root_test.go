package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/codexexec"
	"github.com/andyandymike/done-then/internal/completion"
	"github.com/andyandymike/done-then/internal/platform"
	"github.com/andyandymike/done-then/internal/store"
	"github.com/andyandymike/done-then/internal/supervisor"
)

type testPowerLock struct {
	released bool
}

func (l *testPowerLock) Release() error {
	l.released = true
	return nil
}

func TestVersionCommandReportsConfiguredBuildVersion(t *testing.T) {
	original := Version
	Version = "9.8.7-test"
	t.Cleanup(func() { Version = original })

	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"--version"}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	})
	if exitCode != 0 || stdout.String() != "donethen 9.8.7-test\n" || stderr.Len() != 0 {
		t.Fatalf("version exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunRejectsExecuteWithoutIndependentEvidenceOptIn(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{
		"run",
		"--action", "shutdown",
		"--execute",
		"--",
		"codex", "exec", "prompt",
	}, IO{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &stderr})
	if exitCode != 2 || !strings.Contains(stderr.String(), "allow-agent-only-success") {
		t.Fatalf("Run() exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestRunRejectsUnsafeDelayBeforeStartingCodex(t *testing.T) {
	var stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{
		"run",
		"--action", "shutdown",
		"--delay", "10s",
		"--",
		"codex", "exec", "prompt",
	}, IO{Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &stderr})
	if exitCode != 2 || !strings.Contains(stderr.String(), "between 30s and 1h") {
		t.Fatalf("Run() exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestSplitAtSeparator(t *testing.T) {
	own, agent, err := splitAtSeparator([]string{"--action", "shutdown", "--", "codex", "exec", "prompt"})
	if err != nil || len(own) != 2 || len(agent) != 3 {
		t.Fatalf("splitAtSeparator() = %#v, %#v, %v", own, agent, err)
	}
	if _, _, err := splitAtSeparator([]string{"--action", "shutdown"}); err == nil {
		t.Fatal("splitAtSeparator() accepted a missing separator")
	}
}

func TestReadBounded(t *testing.T) {
	value, err := readBounded(strings.NewReader("abcd"), 4)
	if err != nil || value != "abcd" {
		t.Fatalf("readBounded() = %q, %v", value, err)
	}
	if _, err := readBounded(strings.NewReader("abcde"), 4); err == nil {
		t.Fatal("readBounded() accepted oversized input")
	}
}

func TestRunDryRunEndToEndWithFakeCodexAndVerifier(t *testing.T) {
	t.Setenv("DONETHEN_CLI_CODEX_HELPER", "1")
	t.Setenv("DONETHEN_CLI_VERIFIER_HELPER", "1")
	root := t.TempDir()
	backend := &actions.FakeBackend{}
	deps := testDependencies(root, backend, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{
		"run",
		"--action", "shutdown",
		"--verify-program", os.Args[0],
		"--verify-arg=-test.run=TestCLIVerifierHelperProcess",
		"--verify-arg=--",
		"--codex-path", "fake-codex.exe",
		"--",
		"codex", "exec", "-C", t.TempDir(), "implement the fixture",
	}, IO{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, deps)
	if exitCode != 0 {
		t.Fatalf("runWithDependencies() exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	scheduleCalls, abortCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 0 || abortCalls != 0 {
		t.Fatalf("dry-run backend schedule=%d abort=%d", scheduleCalls, abortCalls)
	}
	jobStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := jobStore.List()
	if err != nil || len(jobs) != 1 || jobs[0].State != supervisor.StateDryRunComplete {
		t.Fatalf("jobs = %#v, %v", jobs, err)
	}
	if !strings.Contains(stdout.String(), "fake Codex stdout") || !strings.Contains(stdout.String(), "fake verifier stdout") {
		t.Fatalf("child output was not forwarded: stdout=%q", stdout.String())
	}
}

func TestRunExecuteAndCancelEndToEndUseOnlyInjectedBackend(t *testing.T) {
	t.Setenv("DONETHEN_CLI_CODEX_HELPER", "1")
	root := t.TempDir()
	backend := &actions.FakeBackend{}
	lock := &testPowerLock{}
	deps := testDependencies(root, backend, lock)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{
		"run",
		"--action", "shutdown",
		"--execute",
		"--allow-agent-only-success",
		"--codex-path", "fake-codex.exe",
		"--",
		"codex", "exec", "implement the fixture",
	}, IO{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, deps)
	if exitCode != 0 {
		t.Fatalf("execute exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	if !lock.released {
		t.Fatal("power lock was not released")
	}
	scheduleCalls, abortCalls, _, comment := backend.Snapshot()
	if scheduleCalls != 1 || abortCalls != 0 || strings.Contains(comment, "fixture result") {
		t.Fatalf("backend schedule=%d abort=%d comment=%q", scheduleCalls, abortCalls, comment)
	}
	jobStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := jobStore.List()
	if err != nil || len(jobs) != 1 || jobs[0].State != supervisor.StateActionScheduled {
		t.Fatalf("jobs = %#v, %v", jobs, err)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = runWithDependencies(context.Background(), []string{"cancel", jobs[0].JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	_, abortCalls, _, _ = backend.Snapshot()
	if abortCalls != 1 {
		t.Fatalf("AbortShutdown calls = %d", abortCalls)
	}
	job, err := jobStore.Load(jobs[0].JobID)
	if err != nil || job.State != supervisor.StateCancelled {
		t.Fatalf("cancelled job = %#v, %v", job, err)
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = runWithDependencies(context.Background(), []string{"cancel", jobs[0].JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || !strings.Contains(stdout.String(), "already cancelled") {
		t.Fatalf("second cancel exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	_, abortCalls, _, _ = backend.Snapshot()
	if abortCalls != 1 {
		t.Fatalf("idempotent cancel made %d abort calls", abortCalls)
	}

	stdout.Reset()
	stderr.Reset()
	exitCode = runWithDependencies(context.Background(), []string{"status", jobs[0].JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || !strings.Contains(stdout.String(), "CANCEL") || !strings.Contains(stdout.String(), "requested") {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestRunStdinPromptAndArtifactCleanupEndToEnd(t *testing.T) {
	t.Setenv("DONETHEN_CLI_CODEX_HELPER", "1")
	capture := filepath.Join(t.TempDir(), "prompt.txt")
	t.Setenv("DONETHEN_CLI_PROMPT_CAPTURE", capture)
	root := t.TempDir()
	backend := &actions.FakeBackend{}
	deps := testDependencies(root, backend, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{
		"run",
		"--action", "shutdown",
		"--codex-path", "fake-codex.exe",
		"--",
		"codex", "exec", "-",
	}, IO{Stdin: strings.NewReader("stdin fixture task"), Stdout: &stdout, Stderr: &stderr}, deps)
	if exitCode != 0 {
		t.Fatalf("stdin run exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "stdin fixture task\n\n") || !strings.Contains(string(data), "DoneThen completion reporting contract") {
		t.Fatalf("captured stdin prompt = %q", data)
	}
	jobStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := jobStore.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %#v, %v", jobs, err)
	}
	artifactDir := filepath.Join(root, "tmp", jobs[0].JobID)
	if _, err := os.Stat(artifactDir); !os.IsNotExist(err) {
		t.Fatalf("default artifact directory still exists: %v", err)
	}
}

func TestRunKeepsArtifactsAndInjectsManagedFlagsBeforeSeparator(t *testing.T) {
	t.Setenv("DONETHEN_CLI_CODEX_HELPER", "1")
	argsCapture := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("DONETHEN_CLI_ARGS_CAPTURE", argsCapture)
	root := t.TempDir()
	backend := &actions.FakeBackend{}
	deps := testDependencies(root, backend, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{
		"run",
		"--action", "shutdown",
		"--keep-artifacts",
		"--codex-path", "fake-codex.exe",
		"--",
		"codex", "exec", "--", "-prompt",
	}, IO{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, deps)
	if exitCode != 0 {
		t.Fatalf("artifact run exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	data, err := os.ReadFile(argsCapture)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.Split(string(data), "\n")
	schemaIndex := argumentIndex(args, "--output-schema")
	outputIndex := argumentIndex(args, "--output-last-message")
	separatorIndex := argumentIndex(args, "--")
	if schemaIndex < 0 || outputIndex < 0 || separatorIndex < 0 || schemaIndex > separatorIndex || outputIndex > separatorIndex {
		t.Fatalf("managed flags were not injected before separator: %#v", args)
	}
	jobStore, err := store.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := jobStore.List()
	if err != nil || len(jobs) != 1 {
		t.Fatalf("jobs = %#v, %v", jobs, err)
	}
	artifactDir := filepath.Join(root, "tmp", jobs[0].JobID)
	for _, name := range []string{"completion-envelope.schema.json", "final-response.json"} {
		if _, err := os.Stat(filepath.Join(artifactDir, name)); err != nil {
			t.Fatalf("kept artifact %s missing: %v", name, err)
		}
	}
}

func TestRunClassifiesMissingCompletionAndNonzeroCodexExit(t *testing.T) {
	tests := []struct {
		name     string
		noResult bool
		exit     bool
		wantCode int
	}{
		{name: "missing completion", noResult: true, wantCode: supervisor.ExitInvalidCompletion},
		{name: "nonzero Codex exit", exit: true, wantCode: supervisor.ExitAgentFailed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DONETHEN_CLI_CODEX_HELPER", "1")
			if test.noResult {
				t.Setenv("DONETHEN_CLI_NO_RESPONSE", "1")
			}
			if test.exit {
				t.Setenv("DONETHEN_CLI_CODEX_EXIT", "1")
			}
			root := t.TempDir()
			backend := &actions.FakeBackend{}
			deps := testDependencies(root, backend, nil)
			var stdout, stderr bytes.Buffer
			exitCode := runWithDependencies(context.Background(), []string{
				"run", "--action", "shutdown", "--codex-path", "fake-codex.exe",
				"--", "codex", "exec", "fixture",
			}, IO{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr}, deps)
			if exitCode != test.wantCode {
				t.Fatalf("run exit=%d want=%d stdout=%q stderr=%q", exitCode, test.wantCode, stdout.String(), stderr.String())
			}
			scheduleCalls, _, _, _ := backend.Snapshot()
			if scheduleCalls != 0 {
				t.Fatalf("failure path scheduled %d actions", scheduleCalls)
			}
		})
	}
}

func testDependencies(root string, backend actions.Backend, lock platform.PowerLock) dependencies {
	return dependencies{
		dataRoot: func() (string, error) { return root, nil },
		resolveCodex: func(string) (codexexec.Executable, error) {
			return codexexec.Executable{
				Path:       os.Args[0],
				PrefixArgs: []string{"-test.run=TestCLICodexHelperProcess", "--"},
			}, nil
		},
		acquirePowerLock: func() (platform.PowerLock, error) {
			if lock == nil {
				return &testPowerLock{}, nil
			}
			return lock, nil
		},
		newActionBackend: func() actions.Backend { return backend },
	}
}

func TestCLICodexHelperProcess(t *testing.T) {
	if os.Getenv("DONETHEN_CLI_CODEX_HELPER") != "1" {
		return
	}
	args := argsAfterSeparator(os.Args)
	if capture := os.Getenv("DONETHEN_CLI_ARGS_CAPTURE"); capture != "" {
		_ = os.WriteFile(capture, []byte(strings.Join(args, "\n")), 0o600)
	}
	prompt := args[len(args)-1]
	if prompt == "-" {
		data, _ := io.ReadAll(os.Stdin)
		prompt = string(data)
	}
	if capture := os.Getenv("DONETHEN_CLI_PROMPT_CAPTURE"); capture != "" {
		_ = os.WriteFile(capture, []byte(prompt), 0o600)
	}
	if os.Getenv("DONETHEN_CLI_CODEX_EXIT") == "1" {
		os.Exit(7)
	}
	responsePath := argumentValue(args, "--output-last-message")
	if responsePath == "" {
		fmt.Fprintln(os.Stderr, "missing output path")
		os.Exit(2)
	}
	envelope := completion.Envelope{
		SchemaVersion: "1",
		Status:        completion.StatusDone,
		Summary:       "fake CLI fixture result",
		Checks: []completion.Check{{
			Name:     "fixture",
			Status:   completion.CheckPassed,
			Evidence: "deterministic",
		}},
		RemainingWork:    []string{},
		ApprovalRequired: false,
	}
	if os.Getenv("DONETHEN_CLI_NO_RESPONSE") == "1" {
		fmt.Fprintln(os.Stdout, "fake Codex stdout without response")
		os.Exit(0)
	}
	data, _ := json.Marshal(envelope)
	if err := os.MkdirAll(filepath.Dir(responsePath), 0o700); err != nil {
		os.Exit(1)
	}
	if err := os.WriteFile(responsePath, data, 0o600); err != nil {
		os.Exit(1)
	}
	fmt.Fprintln(os.Stdout, "fake Codex stdout")
	os.Exit(0)
}

func TestCLIVerifierHelperProcess(t *testing.T) {
	if os.Getenv("DONETHEN_CLI_VERIFIER_HELPER") != "1" {
		return
	}
	fmt.Fprintln(os.Stdout, "fake verifier stdout")
	os.Exit(0)
}

func argsAfterSeparator(args []string) []string {
	for index, arg := range args {
		if arg == "--" && index+1 < len(args) {
			return args[index+1:]
		}
	}
	return nil
}

func argumentValue(args []string, name string) string {
	for index, arg := range args {
		if arg == name && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func argumentIndex(args []string, value string) int {
	for index, arg := range args {
		if arg == value {
			return index
		}
	}
	return -1
}
