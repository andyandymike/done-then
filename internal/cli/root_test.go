package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/codexexec"
	"github.com/andyandymike/done-then/internal/completion"
	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/platform"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/powerpolicy"
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

func TestHookCommandIsSilentForUnrelatedSession(t *testing.T) {
	root := t.TempDir()
	deps := testDependencies(root, &actions.FakeBackend{}, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"hook"}, IO{
		Stdin:  strings.NewReader(`{"session_id":"unbound","turn_id":"turn","hook_event_name":"Stop"}`),
		Stdout: &stdout,
		Stderr: &stderr,
	}, deps)
	if exitCode != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("hook exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestMalformedHookNeverWritesStdoutOrBlocksCodex(t *testing.T) {
	root := t.TempDir()
	deps := testDependencies(root, &actions.FakeBackend{}, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"hook"}, IO{
		Stdin:  strings.NewReader(`not-json`),
		Stdout: &stdout,
		Stderr: &stderr,
	}, deps)
	if exitCode != 0 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "event dropped") {
		t.Fatalf("hook exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestMCPCommandWritesOnlyJSONRPCResponses(t *testing.T) {
	root := t.TempDir()
	deps := testDependencies(root, &actions.FakeBackend{}, nil)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"cli-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n")
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"mcp"}, IO{
		Stdin: strings.NewReader(input), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("mcp exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	for expectedID := 1; expectedID <= 2; expectedID++ {
		var response struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      int             `json:"id"`
			Result  json.RawMessage `json:"result"`
		}
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		if response.JSONRPC != "2.0" || response.ID != expectedID || len(response.Result) == 0 {
			t.Fatalf("MCP response = %#v", response)
		}
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected MCP output: %q (%v)", stdout.String(), err)
	}
}

func TestPublicMCPRejectsVerifiedSuccessExecuteEvenWithInstalledPolicy(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := powerpolicy.Install(root, powerpolicy.Policy{
		SchemaVersion:      1,
		ExecuteEnabled:     true,
		CodexExecutable:    executable,
		ExpectedPluginID:   "done-then@test",
		ExpectedHookHashes: map[string]string{"plugin:done-then:test": "sha256:reviewed"},
	}); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	deps := testDependencies(root, backend, nil)
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"cli-test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"arm","arguments":{"action":"shutdown","trigger_policy":"verified_success","acknowledge_stop_without_success":false,"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute","verifier_profile":"none","allow_agent_only_success":true}}}`,
	}, "\n")
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"mcp"}, IO{
		Stdin: strings.NewReader(input), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || stderr.Len() != 0 {
		t.Fatalf("mcp exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	var initialize map[string]any
	if err := decoder.Decode(&initialize); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	result, ok := response["result"].(map[string]any)
	if !ok || result["isError"] != true {
		t.Fatalf("execute call did not fail closed: %#v", response)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok || structured["reason_code"] != "execute_unavailable" ||
		structured["verified_success_execute_available"] != false ||
		structured["power_action_called"] != false {
		t.Fatalf("execute result = %#v", structured)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("execute rejection content = %#v", result["content"])
	}
	textContent, ok := content[0].(map[string]any)
	if !ok || !strings.Contains(fmt.Sprint(textContent["text"]), "same-host") {
		t.Fatalf("execute rejection did not explain the authority boundary: %#v", content)
	}
	if backend.PreflightCalls != 0 || backend.ScheduleCalls != 0 || backend.CancelCalls != 0 {
		t.Fatalf("public MCP reached the power backend: %#v", backend)
	}
}

func TestStatusAndCancelRecoverObserveOnlyPluginJob(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := pluginstate.Job{
		SchemaVersion:     "1",
		JobID:             jobIdentity.JobID,
		NonceHash:         jobIdentity.NonceHash,
		State:             pluginstate.StateArmPendingBind,
		ReasonCode:        "awaiting_post_tool_hook",
		DryRun:            true,
		Action:            "shutdown",
		DelaySeconds:      120,
		ExpiresAt:         now.Add(time.Hour),
		CreatedAt:         now,
		UpdatedAt:         now,
		Generation:        1,
		VerifierProfile:   "none",
		HookCompatibility: "not_evaluated",
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	deps := testDependencies(root, &actions.FakeBackend{}, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"status", job.JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || !strings.Contains(stdout.String(), "PLUGIN JOB ID") || !strings.Contains(stdout.String(), "0/3") || !strings.Contains(stdout.String(), "agent-only") {
		t.Fatalf("plugin status exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	exitCode = runWithDependencies(context.Background(), []string{"cancel", job.JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || !strings.Contains(stdout.String(), "Observe-only") {
		t.Fatalf("plugin cancel exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	cancelled, err := state.Load(job.JobID)
	if err != nil || cancelled.State != pluginstate.StateCancelled {
		t.Fatalf("cancelled plugin job = %#v, %v", cancelled, err)
	}
}

func TestCancelPersistsPluginActionIntentRequestUntilSchedulerSettles(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	capabilities := actions.Capabilities{
		Platform: "fake", BackendID: "fake", ExecuteSupported: true, CancelScope: actions.CancelScopeJob,
		MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour, ReconcileSupported: true,
	}
	receipt, err := actions.BuildIntentReceipt(jobIdentity.JobID, "shutdown", now, 2*time.Minute, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	deadline := receipt.Deadline
	job := pluginstate.Job{
		SchemaVersion: pluginstate.CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: pluginstate.StateActionIntent, ReasonCode: "power_schedule_outcome_unknown", Action: "shutdown", DelaySeconds: 120,
		TriggerPolicy: pluginstate.TriggerVerifiedSuccess,
		ExpiresAt:     now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, SessionID: "thread-1", ArmTurnID: "turn-1",
		Generation: 2, VerifierProfile: "none", AllowAgentOnlySuccess: true, HookCompatibility: "compatible",
		ArmObserved: true, WorkspaceCWD: root, PowerPolicyFingerprint: "sha256:policy", ActionIntentAt: &now,
		ScheduledFor: &deadline, PowerCapabilities: &capabilities, PowerReceipt: &receipt,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	deps := testDependencies(root, backend, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"cancel", job.JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || !strings.Contains(stdout.String(), "unresolved scheduler boundary") {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionIntent || !updated.CancelRequested ||
		updated.CancelReason != "cli_cancelled_by_user" || updated.CancelResult == nil {
		t.Fatalf("unresolved cancelled intent = %#v", updated)
	}
	_, cancelCalls, _, _ := backend.Snapshot()
	if cancelCalls != 1 {
		t.Fatalf("backend cancel calls = %d", cancelCalls)
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := runWithDependencies(context.Background(), []string{"status", job.JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps); exitCode != 0 || !strings.Contains(stdout.String(), "CANCEL") || !strings.Contains(stdout.String(), "requested") {
		t.Fatalf("status exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
}

func TestDoctorIsReadOnlyAtPowerBoundary(t *testing.T) {
	root := t.TempDir()
	backend := &actions.FakeBackend{}
	deps := testDependencies(root, backend, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"doctor", "--json"}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 {
		t.Fatalf("doctor exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	var report doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if !report.ExecuteAvailable || !report.AfterStopExecuteAvailable || report.VerifiedSuccessExecuteAvailable {
		t.Fatalf("doctor capability split = %#v", report)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 0 || cancelCalls != 0 || backend.ReconcileCalls != 0 || backend.PreflightCalls != 1 {
		t.Fatalf("doctor backend calls: preflight=%d schedule=%d cancel=%d reconcile=%d", backend.PreflightCalls, scheduleCalls, cancelCalls, backend.ReconcileCalls)
	}
}

func TestReconcilePluginReceiptNeverRetriesAction(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	capabilities := actions.Capabilities{
		Platform: "fake", BackendID: "fake", ExecuteSupported: true, CancelScope: actions.CancelScopeJob,
		MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour, ReconcileSupported: true,
	}
	receipt := actions.SealReceipt(actions.Receipt{
		Platform: "fake", BackendID: "fake", BackendVersion: "1", JobID: jobIdentity.JobID, Action: "shutdown",
		RequestedAt: now, ScheduledAt: now, Deadline: now.Add(2 * time.Minute), CancelScope: actions.CancelScopeJob,
	})
	deadline := receipt.Deadline
	job := pluginstate.Job{
		SchemaVersion: pluginstate.CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: pluginstate.StateActionScheduled, ReasonCode: "action_scheduled", Action: "shutdown", DelaySeconds: 120,
		TriggerPolicy: pluginstate.TriggerVerifiedSuccess,
		ExpiresAt:     now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, SessionID: "thread-1", ArmTurnID: "turn-1",
		Generation: 2, VerifierProfile: "none", AllowAgentOnlySuccess: true, HookCompatibility: "compatible",
		ArmObserved: true, WorkspaceCWD: root, PowerPolicyFingerprint: "sha256:policy", ActionIntentAt: &now,
		ScheduledFor: &deadline, PowerCapabilities: &capabilities, PowerReceipt: &receipt,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{ReconcileResult: actions.ReconcileResult{
		State: actions.ReconcileUnverified, CheckedAt: now.Add(3 * time.Minute), Evidence: "fake evidence remains inconclusive",
	}}
	deps := testDependencies(root, backend, nil)
	var stdout, stderr bytes.Buffer
	exitCode := runWithDependencies(context.Background(), []string{"reconcile", job.JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps)
	if exitCode != 0 || !strings.Contains(stdout.String(), string(actions.ReconcileUnverified)) {
		t.Fatalf("reconcile exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionExecutionUnverified || updated.ReconcileResult == nil {
		t.Fatalf("reconciled job = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 0 || cancelCalls != 0 || backend.ReconcileCalls != 1 {
		t.Fatalf("reconcile backend calls: schedule=%d cancel=%d reconcile=%d", scheduleCalls, cancelCalls, backend.ReconcileCalls)
	}
}

func TestEarlyUnverifiedReconcileKeepsPluginCountdownCancellable(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	capabilities := actions.Capabilities{
		Platform: "fake", BackendID: "fake", ExecuteSupported: true, CancelScope: actions.CancelScopeJob,
		MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour, ReconcileSupported: true,
	}
	receipt := actions.SealReceipt(actions.Receipt{
		Platform: "fake", BackendID: "fake", BackendVersion: "1", JobID: jobIdentity.JobID, Action: "shutdown",
		RequestedAt: now, ScheduledAt: now, Deadline: now.Add(5 * time.Minute), CancelScope: actions.CancelScopeJob,
	})
	deadline := receipt.Deadline
	job := pluginstate.Job{
		SchemaVersion: pluginstate.CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: pluginstate.StateActionScheduled, ReasonCode: "action_scheduled", Action: "shutdown", DelaySeconds: 300,
		TriggerPolicy: pluginstate.TriggerVerifiedSuccess,
		ExpiresAt:     now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, SessionID: "thread-1", ArmTurnID: "turn-1",
		Generation: 2, VerifierProfile: "none", AllowAgentOnlySuccess: true, HookCompatibility: "compatible",
		ArmObserved: true, WorkspaceCWD: root, PowerPolicyFingerprint: "sha256:policy", ActionIntentAt: &now,
		ScheduledFor: &deadline, PowerCapabilities: &capabilities, PowerReceipt: &receipt,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{ReconcileResult: actions.ReconcileResult{
		State: actions.ReconcileUnverified, CheckedAt: now.Add(time.Minute), Evidence: "status query was inconclusive",
	}}
	deps := testDependencies(root, backend, nil)
	var stdout, stderr bytes.Buffer
	if exitCode := runWithDependencies(context.Background(), []string{"reconcile", job.JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps); exitCode != 0 {
		t.Fatalf("reconcile exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionScheduled || updated.ReasonCode != "execution_unverified_before_deadline" {
		t.Fatalf("early reconcile discarded cancellation path: %#v", updated)
	}
	stdout.Reset()
	stderr.Reset()
	if exitCode := runWithDependencies(context.Background(), []string{"cancel", job.JobID}, IO{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
	}, deps); exitCode != 0 {
		t.Fatalf("cancel exit=%d stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	updated, err = state.Load(job.JobID)
	if err != nil || updated.State != pluginstate.StateCancelled || backend.CancelCalls != 1 {
		t.Fatalf("cancelled job=%#v cancel_calls=%d err=%v", updated, backend.CancelCalls, err)
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
	registerRetryingTempCleanup(t, root)
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

func registerRetryingTempCleanup(t *testing.T, path string) {
	t.Helper()
	t.Cleanup(func() {
		var err error
		for attempt := 0; attempt < 20; attempt++ {
			err = os.RemoveAll(path)
			if err == nil {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Errorf("remove transient test directory %s: %v", path, err)
	})
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
