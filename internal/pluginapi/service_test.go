package pluginapi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/filetrust"
	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/verifierprofile"
)

func TestPolicyCapabilitiesSeparatePlatformAndAuthorityReadiness(t *testing.T) {
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithOptions(state, Options{
		PolicyCapabilities: map[pluginstate.TriggerPolicy]PolicyCapability{
			pluginstate.TriggerAfterStop: {
				BuildSupported: true, BackendSupported: true, BackendPreflightPassed: true,
				ExecuteReady: false, UnavailableReason: "stop_arbitration_unavailable",
			},
			pluginstate.TriggerAfterAllStop: {
				BuildSupported: true, BackendSupported: true, BackendPreflightPassed: true,
				ExecuteReady: false, UnavailableReason: "stop_arbitration_unavailable",
			},
			pluginstate.TriggerVerifiedSuccess: {
				BuildSupported: true, BackendSupported: true, BackendPreflightPassed: true,
				ExecuteReady: false, UnavailableReason: "verified_success_authority_unavailable",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := service.Call(context.Background(), "status", json.RawMessage(`{}`))
	if result.IsError || result.Structured["execute_available"] != false {
		t.Fatalf("status capabilities = %#v", result)
	}
	build := result.Structured["build_supported_by_policy"].(map[string]bool)
	backend := result.Structured["backend_preflight_passed_by_policy"].(map[string]bool)
	ready := result.Structured["execute_ready_by_policy"].(map[string]bool)
	reasons := result.Structured["execute_unavailable_reasons_by_policy"].(map[string]string)
	if !build["after_stop"] || !backend["after_stop"] || ready["after_stop"] ||
		reasons["after_stop"] != "stop_arbitration_unavailable" {
		t.Fatalf("after_stop capability split: build=%#v backend=%#v ready=%#v reasons=%#v", build, backend, ready, reasons)
	}
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":true,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !arm.IsError || arm.Structured["reason_code"] != "stop_arbitration_unavailable" ||
		arm.Structured["power_action_called"] != false {
		t.Fatalf("Stop execute did not fail closed: %#v", arm)
	}
}

type fakeLauncher struct {
	jobID string
	calls int
}

func (l *fakeLauncher) Launch(jobID string) (int, error) {
	l.jobID = jobID
	l.calls++
	return 1234, nil
}

type fakeRecoveryLauncher struct {
	fakeLauncher
	cancelCalls int
	bindingID   string
	reason      string
}

func (l *fakeRecoveryLauncher) EnsureCancelWorker(_ string, bindingID, reason string) (int, error) {
	l.cancelCalls++
	l.bindingID = bindingID
	l.reason = reason
	return 5678, nil
}

func TestAfterAllStopExecuteReservesEveryTargetBeforeLaunchingOneSupervisor(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	backend := &actions.FakeBackend{}
	service, err := NewWithOptions(state, Options{
		AfterStopExecuteAvailable: true, Workspace: root, Launcher: launcher, Backend: backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_all_stop",
		"target_session_ids":["target-a","target-b"],
		"acknowledge_stop_without_success":true,"acknowledge_barrier_across_turns":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !rejected.IsError || rejected.Structured["reason_code"] != "barrier_across_turns_acknowledgement_required" {
		t.Fatalf("unacknowledged barrier = %#v", rejected)
	}
	armed := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_all_stop",
		"target_session_ids":["target-a","target-b"],
		"acknowledge_stop_without_success":true,"acknowledge_barrier_across_turns":true,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if armed.IsError {
		t.Fatalf("arm barrier = %#v", armed)
	}
	jobID := armed.Structured["job_id"].(string)
	job, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.TriggerPolicy != pluginstate.TriggerAfterAllStop || !job.TargetReservationsCommitted ||
		job.TargetIndexesReady || len(job.StopTargets) != 2 || launcher.calls != 1 || launcher.jobID != jobID {
		t.Fatalf("reserved barrier=%#v launcher=%#v", job, launcher)
	}
	if job.StopTargets[0].SessionHash != identity.SHA256([]byte("target-a")) ||
		job.StopTargets[1].SessionHash != identity.SHA256([]byte("target-b")) {
		t.Fatalf("target order/hash = %#v", job.StopTargets)
	}
	encodedJob, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedJob), "target-a") || strings.Contains(string(encodedJob), "target-b") {
		t.Fatalf("barrier job persisted raw target ids: %s", encodedJob)
	}
	if backend.PreflightCalls != 1 {
		t.Fatalf("preflight calls = %d", backend.PreflightCalls)
	}
	conflict := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_all_stop",
		"target_session_ids":["target-b","target-c"],
		"acknowledge_stop_without_success":false,"acknowledge_barrier_across_turns":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !conflict.IsError || conflict.Structured["reason_code"] != "target_session_conflict" || launcher.calls != 1 {
		t.Fatalf("overlapping barrier = %#v launcher=%#v", conflict, launcher)
	}
}

func TestAfterAllStopRejectsDuplicateAndLegacyBarrierArguments(t *testing.T) {
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	duplicate := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_all_stop","target_session_ids":["same","same"],
		"acknowledge_stop_without_success":false,"acknowledge_barrier_across_turns":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !duplicate.IsError || duplicate.Structured["reason_code"] != "duplicate_target_session" {
		t.Fatalf("duplicate targets = %#v", duplicate)
	}
	legacy := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","target_session_ids":["a","b"],
		"acknowledge_stop_without_success":false,"acknowledge_barrier_across_turns":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !legacy.IsError || legacy.Structured["reason_code"] != "barrier_arguments_not_applicable" {
		t.Fatalf("legacy barrier args = %#v", legacy)
	}
}

func TestExecuteArmFailsClosedWithoutCreatingJob(t *testing.T) {
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	result := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":3600,
		"trigger_policy":"after_stop","acknowledge_stop_without_success":true,
		"mode":"execute","verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !result.IsError || result.Structured["reason_code"] != "execute_unavailable" {
		t.Fatalf("execute arm result = %#v", result)
	}
	jobs, err := state.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("execute rejection created jobs: %#v", jobs)
	}
}

func TestExecuteArmRequiresTwoMinuteCancellationWindow(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := verifierprofile.New(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithOptions(state, Options{
		ExecuteAvailable: true, Workspace: root, Profiles: profiles, Launcher: &fakeLauncher{},
		Backend: &actions.FakeBackend{}, PowerPolicyFingerprint: "sha256:policy", AllowAgentOnlySuccess: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":119,"expires_in_seconds":3600,
		"trigger_policy":"verified_success","acknowledge_stop_without_success":false,
		"mode":"execute","verifier_profile":"none","allow_agent_only_success":true
	}`))
	if !result.IsError || result.Structured["reason_code"] != "invalid_delay" {
		t.Fatalf("short execute arm = %#v", result)
	}
	j, err := state.List()
	if err != nil || len(j) != 0 {
		t.Fatalf("short execute arm created jobs: %#v, %v", j, err)
	}
}

func TestAfterStopExecuteRequiresAcknowledgementAndOnlyPreflightsAtArm(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	backend := &actions.FakeBackend{}
	service, err := NewWithOptions(state, Options{
		AfterStopExecuteAvailable: true,
		Workspace:                 root,
		Launcher:                  launcher,
		Backend:                   backend,
	})
	if err != nil {
		t.Fatal(err)
	}
	rejected := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !rejected.IsError || rejected.Structured["reason_code"] != "stop_without_success_acknowledgement_required" {
		t.Fatalf("unacknowledged after-stop arm = %#v", rejected)
	}
	armed := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":true,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if armed.IsError {
		t.Fatalf("acknowledged after-stop arm = %#v", armed)
	}
	jobID := armed.Structured["job_id"].(string)
	job, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.TriggerPolicy != pluginstate.TriggerAfterStop || !job.StopWithoutSuccessAck ||
		job.PowerPolicyFingerprint != "" || launcher.jobID != jobID {
		t.Fatalf("after-stop job = %#v; launcher=%q", job, launcher.jobID)
	}
	scheduleCalls, _, _, _ := backend.Snapshot()
	if backend.PreflightCalls != 1 || scheduleCalls != 0 {
		t.Fatalf("arm backend calls: preflight=%d schedule=%d", backend.PreflightCalls, scheduleCalls)
	}
	if backend.LastRequest.Comment != actions.AfterStopPowerComment(jobID) {
		t.Fatalf("after-stop preflight comment = %q", backend.LastRequest.Comment)
	}
}

func TestArmExpiryCannotExceedOneDay(t *testing.T) {
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	result := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":86401,
		"trigger_policy":"after_stop","acknowledge_stop_without_success":false,
		"mode":"dry_run","verifier_profile":"none","allow_agent_only_success":false
	}`))
	if !result.IsError || result.Structured["reason_code"] != "invalid_expiry" {
		t.Fatalf("long expiry arm = %#v", result)
	}
}

func TestFinishRejectsNonDoneCompletionAndKeepsPowerUnavailable(t *testing.T) {
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":3600,
		"trigger_policy":"verified_success","acknowledge_stop_without_success":false,
		"mode":"dry_run","verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	if _, _, err := state.BindSession(jobID, "session", "turn", "", eventKeyForTest("a")); err != nil {
		t.Fatal(err)
	}
	finish := service.Call(context.Background(), "finish", json.RawMessage(`{
		"job_id":"`+jobID+`",
		"completion":{"schema_version":"1","status":"partial","summary":"work remains","checks":[],"remaining_work":["test"],"approval_required":false}
	}`))
	if !finish.IsError || finish.Structured["reason_code"] != "not_done" {
		t.Fatalf("partial finish = %#v", finish)
	}
	job, err := state.Load(jobID)
	if err != nil || job.State != pluginstate.StateNotDone || !job.DryRun {
		t.Fatalf("partial job = %#v, %v", job, err)
	}
	if finish.Structured["power_action_called"] != false || finish.Structured["execute_available"] != false {
		t.Fatalf("finish crossed action boundary: %#v", finish.Structured)
	}
}

func TestFinishRequiresVerifierOrExplicitAgentOnlyBoundary(t *testing.T) {
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":3600,
		"trigger_policy":"verified_success","acknowledge_stop_without_success":false,
		"mode":"dry_run","verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	if _, _, err := state.BindSession(jobID, "session", "turn", "", eventKeyForTest("b")); err != nil {
		t.Fatal(err)
	}
	finish := service.Call(context.Background(), "finish", json.RawMessage(`{
		"job_id":"`+jobID+`",
		"completion":{"schema_version":"1","status":"done","summary":"done","checks":[],"remaining_work":[],"approval_required":false}
	}`))
	if !finish.IsError || finish.Structured["reason_code"] != "independent_evidence_required" {
		t.Fatalf("agent-only finish = %#v", finish)
	}
	job, err := state.Load(jobID)
	if err != nil || job.State != pluginstate.StateVerificationFailed {
		t.Fatalf("agent-only job = %#v, %v", job, err)
	}
}

func TestExecuteArmStartsOneShotSupervisorWithoutSchedulingPower(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := verifierprofile.New(root)
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeLauncher{}
	backend := &actions.FakeBackend{}
	service, err := NewWithOptions(state, Options{
		ExecuteAvailable: true, Workspace: root, Profiles: profiles, Launcher: launcher,
		Backend: backend, PowerPolicyFingerprint: "sha256:policy",
		AllowAgentOnlySuccess: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":3600,
		"trigger_policy":"verified_success","acknowledge_stop_without_success":false,
		"mode":"execute","verifier_profile":"none","allow_agent_only_success":true
	}`))
	if result.IsError || result.Structured["execute_available"] != true {
		t.Fatalf("execute arm = %#v", result)
	}
	jobID := result.Structured["job_id"].(string)
	job, err := state.Load(jobID)
	if err != nil || launcher.jobID != jobID || job.SupervisorPID != 1234 || job.PowerPolicyFingerprint != "sha256:policy" {
		t.Fatalf("armed execute job=%#v launcher=%q err=%v", job, launcher.jobID, err)
	}
	scheduleCalls, _, _, _ := backend.Snapshot()
	if scheduleCalls != 0 {
		t.Fatalf("arm scheduled %d power actions", scheduleCalls)
	}
}

func TestCancelSettlesLegacyIntentAfterPositiveBackendCancellation(t *testing.T) {
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
		Platform: "fake", BackendID: "fake", ExecuteSupported: true,
		CancelScope: actions.CancelScopeJob, MinimumDelay: 30 * time.Second,
		MaximumDelay: time.Hour, ReconcileSupported: true,
	}
	receipt, err := actions.BuildIntentReceipt(jobIdentity.JobID, "shutdown", now, 2*time.Minute, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	deadline := receipt.Deadline
	job := pluginstate.Job{
		SchemaVersion: pluginstate.CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: pluginstate.StateActionIntent, ReasonCode: "action_intent_recorded", Action: "shutdown", DelaySeconds: 120,
		TriggerPolicy: pluginstate.TriggerVerifiedSuccess,
		ExpiresAt:     now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, SessionID: "thread", ArmTurnID: "turn",
		Generation: 2, VerifierProfile: "none", AllowAgentOnlySuccess: true, HookCompatibility: "compatible",
		ArmObserved: true, WorkspaceCWD: root, PowerPolicyFingerprint: "sha256:policy", ActionIntentAt: &now,
		ScheduledFor: &deadline, PowerCapabilities: &capabilities, PowerReceipt: &receipt,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	service, err := NewWithOptions(state, Options{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	result := service.Call(context.Background(), "cancel", json.RawMessage(`{"job_id":"`+job.JobID+`"}`))
	if result.IsError {
		t.Fatalf("cancel = %#v", result)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateCancelled || !updated.CancelRequested ||
		updated.CancelReason != "mcp_cancelled_by_user" || updated.CancelResult == nil {
		t.Fatalf("settled cancelled intent = %#v", updated)
	}
	_, cancelCalls, _, _ := backend.Snapshot()
	if cancelCalls != 1 {
		t.Fatalf("backend cancel calls = %d", cancelCalls)
	}
}

func TestMCPCancelHandsPreCallRecoveryToMachineFencedWorkerWithoutBackendCancel(t *testing.T) {
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
		SchemaVersion: pluginstate.CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: pluginstate.StateStopObserved, ReasonCode: "after_stop_observed_awaiting_countdown", Action: "shutdown",
		TriggerPolicy: pluginstate.TriggerAfterStop, StopWithoutSuccessAck: true, DelaySeconds: 120,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Generation: 1,
		SessionID: "mcp-pre-call", ArmTurnID: "turn-1", CurrentTurnID: "turn-1", StopTurnID: "turn-1",
		VerifierProfile: "none", HookCompatibility: "session_bound", ArmObserved: true, WorkspaceCWD: root,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	request := actions.PowerRequest{JobID: job.JobID, Action: job.Action, Delay: 2 * time.Minute, Comment: "test", RequestedAt: now}
	capabilities, err := backend.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := actions.BuildIntentReceipt(job.JobID, job.Action, now, request.Delay, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.PersistRecoveryEnvelope(job, intent, now); err != nil {
		t.Fatal(err)
	}
	deadline := intent.Deadline.UTC()
	job, _, err = state.UpdateJob(job.JobID, "test.intent", "", func(current *pluginstate.Job, _ time.Time) error {
		current.State = pluginstate.StateActionIntent
		current.ReasonCode = "action_intent_recorded"
		current.ActionIntentAt = &now
		current.ScheduledFor = &deadline
		current.PowerCapabilities = &capabilities
		current.PowerReceipt = &intent
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &fakeRecoveryLauncher{}
	service, err := NewWithOptions(state, Options{Backend: backend, Launcher: launcher})
	if err != nil {
		t.Fatal(err)
	}
	result := service.Call(context.Background(), "cancel", json.RawMessage(`{"job_id":"`+job.JobID+`"}`))
	if result.IsError || !strings.Contains(result.Text, "machine-fenced") {
		t.Fatalf("MCP pre-call cancel = %#v", result)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionIntent || !updated.CancelRequested ||
		launcher.cancelCalls != 1 || launcher.bindingID != job.JobID || launcher.reason != "mcp_cancelled_by_user" {
		t.Fatalf("MCP recovery handoff job=%#v launcher=%#v", updated, launcher)
	}
	_, cancelCalls, _, _ := backend.Snapshot()
	if cancelCalls != 0 {
		t.Fatalf("MCP pre-call cancellation called backend %d times", cancelCalls)
	}
}

func TestFinishRunsFixedRegisteredVerifierBeforeReadyState(t *testing.T) {
	root := t.TempDir()
	program, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	profileDir := filepath.Join(root, "verifiers")
	if err := filetrust.EnsureOwnerControlledDirectory(profileDir, "test verifier directory"); err != nil {
		t.Fatal(err)
	}
	profile := map[string]any{
		"schema_version": 1, "id": "tests", "program": program,
		"args":              []string{"-test.run=TestPluginVerifierHelperProcess", "--", "donethen-plugin-verifier-helper"},
		"working_directory": "armed_workspace", "timeout_seconds": 30, "environment_policy": "minimal",
	}
	profileData, _ := json.Marshal(profile)
	profilePath := filepath.Join(profileDir, "tests.json")
	if err := os.WriteFile(profilePath, profileData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := filetrust.HardenOwnerControlled(profilePath); err != nil {
		t.Fatal(err)
	}
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := verifierprofile.New(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewWithOptions(state, Options{
		ExecuteAvailable: true, Workspace: root, Profiles: profiles, Launcher: &fakeLauncher{},
		Backend: &actions.FakeBackend{}, PowerPolicyFingerprint: "sha256:policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":3600,
		"trigger_policy":"verified_success","acknowledge_stop_without_success":false,
		"mode":"execute","verifier_profile":"tests","allow_agent_only_success":false
	}`))
	if arm.IsError {
		t.Fatalf("arm = %#v", arm)
	}
	jobID := arm.Structured["job_id"].(string)
	if _, _, err := state.BindSession(jobID, "thread", "turn", root, eventKeyForTest("e")); err != nil {
		t.Fatal(err)
	}
	finish := service.Call(context.Background(), "finish", json.RawMessage(`{
		"job_id":"`+jobID+`",
		"completion":{"schema_version":"1","status":"done","summary":"done","checks":[],"remaining_work":[],"approval_required":false}
	}`))
	if finish.IsError {
		t.Fatalf("finish = %#v", finish)
	}
	job, err := state.Load(jobID)
	if err != nil || job.State != pluginstate.StateReadyPendingStop || !job.VerifierPassed || job.VerifierExitCode == nil || *job.VerifierExitCode != 0 {
		t.Fatalf("verified job = %#v, %v", job, err)
	}
}

func TestPluginVerifierHelperProcess(t *testing.T) {
	if !slices.Contains(os.Args, "donethen-plugin-verifier-helper") {
		return
	}
	os.Exit(0)
}

func eventKeyForTest(seed string) string {
	value := ""
	for len(value) < 64 {
		value += seed
	}
	return value[:64]
}
