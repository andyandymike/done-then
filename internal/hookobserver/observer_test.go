package hookobserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/pluginapi"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

type observerTestLauncher struct{}

func (observerTestLauncher) Launch(string) (int, error) { return 4242, nil }

type observerCancelLauncher struct {
	state               *pluginstate.Store
	calls               int
	jobID               string
	bindingID           string
	reason              string
	durableBeforeLaunch bool
}

func (l *observerCancelLauncher) EnsureCancelWorker(jobID, bindingID, reason string) (int, error) {
	l.calls++
	l.jobID = jobID
	l.bindingID = bindingID
	l.reason = reason
	_, pending, err := l.state.PendingRevocation(jobID)
	l.durableBeforeLaunch = err == nil && pending
	return 4243, nil
}

func TestHookStateLockRetryUsesTheProductionAttemptBudget(t *testing.T) {
	attempts := 0
	err := retryStateLock(func() error {
		attempts++
		if attempts < hookStateLockAttempts {
			return pluginstate.ErrLockTimeout
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != hookStateLockAttempts {
		t.Fatalf("state lock attempts = %d, want %d", attempts, hookStateLockAttempts)
	}
}

func TestHookEventKeyUsesUnambiguousTupleEncoding(t *testing.T) {
	first := hookInput{SessionID: "a\x00b", TurnID: "c", HookEventName: "Stop"}
	second := hookInput{SessionID: "a", TurnID: "b\x00c", HookEventName: "Stop"}
	if hookEventKey(first, "dt_TEST") == hookEventKey(second, "dt_TEST") {
		t.Fatal("length-ambiguous hook tuples produced the same event key")
	}
}

func TestObserveOnlyLifecycleBindsFinishesAndRecordsStop(t *testing.T) {
	state, service, observer := testComponents(t)
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown",
		"delay_seconds":120,
		"expires_in_seconds":3600,
		"trigger_policy":"verified_success",
		"acknowledge_stop_without_success":false,
		"mode":"dry_run",
		"verifier_profile":"none",
		"allow_agent_only_success":true
	}`))
	if arm.IsError {
		t.Fatalf("arm failed: %#v", arm)
	}
	jobID := arm.Structured["job_id"].(string)
	observeTool(t, observer, "session-1", "turn-1", "call-arm", "arm", json.RawMessage(`{}`), arm)

	job, err := state.Load(jobID)
	if err != nil || job.State != pluginstate.StateArmed || !job.ArmObserved {
		t.Fatalf("bound job = %#v, %v", job, err)
	}

	finishInput := json.RawMessage(`{
		"job_id":"` + jobID + `",
		"completion":{
			"schema_version":"1",
			"status":"done",
			"summary":"All requested work is complete.",
			"checks":[{"name":"tests","status":"passed","evidence":"go test ./..."}],
			"remaining_work":[],
			"approval_required":false
		}
	}`)
	finish := service.Call(context.Background(), "finish", finishInput)
	if finish.IsError {
		t.Fatalf("finish failed: %#v", finish)
	}
	observeTool(t, observer, "session-1", "turn-1", "call-finish", "finish", finishInput, finish)

	stop := `{"session_id":"session-1","turn_id":"turn-1","hook_event_name":"Stop","stop_hook_active":false}`
	if err := observer.Handle(strings.NewReader(stop)); err != nil {
		t.Fatal(err)
	}
	job, err = state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != pluginstate.StateStopObserved || job.StopTurnID != "turn-1" {
		t.Fatalf("stopped job = %#v", job)
	}
	if job.ReasonCode != "matching_stop_observed_no_action" || !job.DryRun {
		t.Fatalf("stop unexpectedly crossed action boundary: %#v", job)
	}
	eventLog, err := os.ReadFile(filepath.Join(state.Root(), "events", jobID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"session-1", "turn-1", "All requested work is complete"} {
		if strings.Contains(string(eventLog), secret) {
			t.Fatalf("redacted event log contains %q: %s", secret, eventLog)
		}
	}

	keyCount := len(job.ProcessedEventKeys)
	if err := observer.Handle(strings.NewReader(stop)); err != nil {
		t.Fatal(err)
	}
	duplicate, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.State != pluginstate.StateStopObserved || len(duplicate.ProcessedEventKeys) != keyCount {
		t.Fatalf("duplicate Stop was not idempotent: %#v", duplicate)
	}
}

func TestUserPromptInvalidatesReadyEvidence(t *testing.T) {
	state, service, observer := testComponents(t)
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":3600,
		"trigger_policy":"verified_success","acknowledge_stop_without_success":false,
		"mode":"dry_run","verifier_profile":"none","allow_agent_only_success":true
	}`))
	jobID := arm.Structured["job_id"].(string)
	observeTool(t, observer, "session-2", "turn-1", "call-arm", "arm", json.RawMessage(`{}`), arm)
	finishInput := json.RawMessage(`{
		"job_id":"` + jobID + `",
		"completion":{"schema_version":"1","status":"done","summary":"done","checks":[],"remaining_work":[],"approval_required":false}
	}`)
	finish := service.Call(context.Background(), "finish", finishInput)
	observeTool(t, observer, "session-2", "turn-1", "call-finish", "finish", finishInput, finish)

	ready, err := state.Load(jobID)
	if err != nil || ready.State != pluginstate.StateReadyPendingStop || !ready.FinishObserved {
		t.Fatalf("ready job = %#v, %v", ready, err)
	}
	prompt := `{"session_id":"session-2","turn_id":"turn-2","hook_event_name":"UserPromptSubmit","prompt":"more work"}`
	if err := observer.Handle(strings.NewReader(prompt)); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateArmed || updated.CurrentTurnID != "turn-2" {
		t.Fatalf("prompt did not resume armed job: %#v", updated)
	}
	if updated.CompletionEvidenceHash != "" || updated.ReadyTurnID != "" || updated.FinishObserved {
		t.Fatalf("prompt retained stale completion evidence: %#v", updated)
	}
	if updated.Generation <= ready.Generation {
		t.Fatalf("generation did not advance: before=%d after=%d", ready.Generation, updated.Generation)
	}
}

func TestStopWithoutReadyIsObserverOnlyNoOp(t *testing.T) {
	state, service, observer := testComponents(t)
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","delay_seconds":120,"expires_in_seconds":3600,
		"trigger_policy":"verified_success","acknowledge_stop_without_success":false,
		"mode":"dry_run","verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	observeTool(t, observer, "session-3", "turn-1", "call-arm", "arm", json.RawMessage(`{}`), arm)
	if err := observer.Handle(strings.NewReader(`{"session_id":"session-3","turn_id":"turn-1","hook_event_name":"Stop"}`)); err != nil {
		t.Fatal(err)
	}
	job, err := state.Load(jobID)
	if err != nil || job.State != pluginstate.StateArmed {
		t.Fatalf("unready Stop changed authority: %#v, %v", job, err)
	}
}

func TestAfterStopDryRunCompletesOnFirstMatchingStopWithoutFinish(t *testing.T) {
	state, service, observer := testComponents(t)
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if arm.IsError {
		t.Fatalf("arm failed: %#v", arm)
	}
	jobID := arm.Structured["job_id"].(string)
	observeTool(t, observer, "session-after-stop", "turn-1", "call-arm", "arm", json.RawMessage(`{}`), arm)
	if err := observer.Handle(strings.NewReader(`{
		"session_id":"session-after-stop","turn_id":"turn-1","hook_event_name":"Stop","stop_hook_active":false
	}`)); err != nil {
		t.Fatal(err)
	}
	job, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != pluginstate.StateDryRunComplete || job.StopTurnID != "turn-1" ||
		job.ReasonCode != "after_stop_observed_no_action" {
		t.Fatalf("after-stop dry-run = %#v", job)
	}
	finish := service.Call(context.Background(), "finish", json.RawMessage(`{
		"job_id":"`+jobID+`",
		"completion":{"schema_version":"1","status":"done","summary":"ignored","checks":[],"remaining_work":[],"approval_required":false}
	}`))
	if !finish.IsError || finish.Structured["reason_code"] != "finish_not_required" {
		t.Fatalf("after-stop finish = %#v", finish)
	}
}

func TestAfterStopExecuteUsesHookWorkspaceAndWaitsForMatchingStop(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	service, err := pluginapi.NewWithOptions(state, pluginapi.Options{
		AfterStopExecuteAvailable: true,
		Workspace:                 root,
		Launcher:                  observerTestLauncher{},
		Backend:                   &actions.FakeBackend{},
	})
	if err != nil {
		t.Fatal(err)
	}
	observer, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":true,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if arm.IsError {
		t.Fatalf("arm failed: %#v", arm)
	}
	jobID := arm.Structured["job_id"].(string)
	hookWorkspace := filepath.Join(root, "actual-workspace")
	observeToolAtWorkspace(t, observer, "session-workspace", "turn-1", "call-arm", "arm", hookWorkspace, json.RawMessage(`{}`), arm)

	bound, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if bound.State != pluginstate.StateArmed || bound.WorkspaceCWD != filepath.Clean(hookWorkspace) {
		t.Fatalf("hook-bound after-stop job = %#v", bound)
	}
	stop, err := json.Marshal(map[string]any{
		"session_id": "session-workspace", "turn_id": "turn-1", "cwd": hookWorkspace,
		"hook_event_name": "Stop", "stop_hook_active": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.Handle(strings.NewReader(string(stop))); err != nil {
		t.Fatal(err)
	}
	stopped, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.State != pluginstate.StateStopObserved || stopped.StopTurnID != "turn-1" {
		t.Fatalf("matching after-stop execute job = %#v", stopped)
	}
	if err := observer.Handle(strings.NewReader(`{
		"session_id":"session-workspace","hook_event_name":"SessionEnd","reason":"exit"
	}`)); err != nil {
		t.Fatal(err)
	}
	afterSessionEnd, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if afterSessionEnd.State != pluginstate.StateStopObserved || afterSessionEnd.StopTurnID != "turn-1" {
		t.Fatalf("SessionEnd revoked accepted after-stop grant = %#v", afterSessionEnd)
	}
	if _, pending, err := state.PendingRevocation(jobID); err != nil || pending {
		t.Fatalf("accepted after-stop SessionEnd created revocation: pending=%t err=%v", pending, err)
	}
}

func TestAfterStopGrantIsCancelledByNextUserPrompt(t *testing.T) {
	state, service, observer := testComponents(t)
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	observeTool(t, observer, "session-cancel", "turn-1", "call-arm", "arm", json.RawMessage(`{}`), arm)
	if err := observer.Handle(strings.NewReader(`{
		"session_id":"session-cancel","turn_id":"turn-2","hook_event_name":"UserPromptSubmit","prompt":"continue"
	}`)); err != nil {
		t.Fatal(err)
	}
	job, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != pluginstate.StateCancelled || job.ReasonCode != "new_prompt_cancelled_after_stop_grant" {
		t.Fatalf("continued after-stop job = %#v", job)
	}
}

func TestAfterStopGrantIsCancelledInsideStopHookContinuation(t *testing.T) {
	state, service, observer := testComponents(t)
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	observeTool(t, observer, "session-hook-loop", "turn-1", "call-arm", "arm", json.RawMessage(`{}`), arm)
	if err := observer.Handle(strings.NewReader(`{
		"session_id":"session-hook-loop","turn_id":"turn-1","hook_event_name":"Stop","stop_hook_active":true
	}`)); err != nil {
		t.Fatal(err)
	}
	job, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if job.State != pluginstate.StateCancelled || job.ReasonCode != "stop_hook_continuation_cancelled_grant" {
		t.Fatalf("stop-hook continuation job = %#v", job)
	}
}

func TestUserPromptPreservesScheduledReceiptAndRequestsCancellation(t *testing.T) {
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
		State: pluginstate.StateArmPendingBind, ReasonCode: "awaiting_hook", Action: "shutdown", DelaySeconds: 120,
		TriggerPolicy: pluginstate.TriggerVerifiedSuccess,
		ExpiresAt:     now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Generation: 1,
		VerifierProfile: "none", AllowAgentOnlySuccess: true, HookCompatibility: "not_evaluated",
		WorkspaceCWD: root, PowerPolicyFingerprint: "sha256:policy",
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	job, _, err = state.BindSession(job.JobID, "session-power", "turn-1", root, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	capabilities := actions.Capabilities{
		Platform: "fake", BackendID: "fake", ExecuteSupported: true, CancelScope: actions.CancelScopeJob,
		MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour,
	}
	receipt := actions.SealReceipt(actions.Receipt{
		Platform: "fake", BackendID: "fake", BackendVersion: "1", JobID: job.JobID, Action: job.Action,
		RequestedAt: now, ScheduledAt: now, Deadline: now.Add(2 * time.Minute), ExternalToken: "fixed-token",
		CancelScope: actions.CancelScopeJob,
	})
	deadline := receipt.Deadline
	job, _, err = state.UpdateJob(job.JobID, "test.power.scheduled", "", func(job *pluginstate.Job, _ time.Time) error {
		job.State = pluginstate.StateActionScheduled
		job.ActionIntentAt = &now
		job.ScheduledFor = &deadline
		job.PowerCapabilities = &capabilities
		job.PowerReceipt = &receipt
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelLauncher := &observerCancelLauncher{state: state}
	observer, err := NewWithOptions(state, Options{CancelLauncher: cancelLauncher})
	if err != nil {
		t.Fatal(err)
	}
	prompt := `{"session_id":"session-power","turn_id":"turn-2","hook_event_name":"UserPromptSubmit","prompt":"continue"}`
	if err := observer.Handle(strings.NewReader(prompt)); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionScheduled || !updated.CancelRequested || updated.PowerReceipt == nil ||
		updated.PowerReceipt.Checksum != receipt.Checksum || updated.Generation <= job.Generation {
		t.Fatalf("continued scheduled job = %#v", updated)
	}
	if cancelLauncher.calls != 1 || cancelLauncher.jobID != job.JobID || cancelLauncher.bindingID != job.JobID ||
		!cancelLauncher.durableBeforeLaunch {
		t.Fatalf("cancel worker handoff = %#v", cancelLauncher)
	}
}

func TestAfterAllStopReopensOnlyTheTargetThatContinues(t *testing.T) {
	state, service, observer := testComponents(t)
	root := filepath.Dir(state.Root())
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_all_stop",
		"target_session_ids":["target-a","target-b"],
		"acknowledge_stop_without_success":false,"acknowledge_barrier_across_turns":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	if arm.IsError {
		t.Fatalf("arm barrier = %#v", arm)
	}
	jobID := arm.Structured["job_id"].(string)
	observeToolAtWorkspace(t, observer, "controller", "controller-turn", "call-arm-barrier", "arm", root, json.RawMessage(`{}`), arm)

	targetAWorkspace := filepath.Join(root, "repo-a")
	targetBWorkspace := filepath.Join(root, "repo-b")
	observeHook(t, observer, map[string]any{
		"session_id": "target-a", "turn_id": "turn-a-1", "cwd": targetAWorkspace,
		"hook_event_name": "Stop", "stop_hook_active": false,
	})
	partial, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	stopped, _ := partial.BarrierProgress()
	if partial.State != pluginstate.StateArmed || stopped != 1 || partial.ReasonCode != "after_all_stop_barrier_partial" {
		t.Fatalf("first target Stop = %#v", partial)
	}

	observeHook(t, observer, map[string]any{
		"session_id": "target-a", "turn_id": "turn-a-2", "cwd": targetAWorkspace,
		"hook_event_name": "UserPromptSubmit", "prompt": "continue target a",
	})
	observeHook(t, observer, map[string]any{
		"session_id": "target-b", "turn_id": "turn-b-1", "cwd": targetBWorkspace,
		"hook_event_name": "Stop", "stop_hook_active": false,
	})
	reopened, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	stopped, _ = reopened.BarrierProgress()
	if reopened.State != pluginstate.StateArmed || stopped != 1 ||
		reopened.StopTargets[0].CurrentTurnHash != identity.SHA256([]byte("turn-a-2")) || reopened.StopTargets[0].Stopped() {
		t.Fatalf("reopened barrier = %#v", reopened)
	}

	// A replay from target A's old turn cannot satisfy the barrier.
	observeHook(t, observer, map[string]any{
		"session_id": "target-a", "turn_id": "turn-a-1", "cwd": targetAWorkspace,
		"hook_event_name": "Stop", "stop_hook_active": false,
	})
	stillPending, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if stillPending.State != pluginstate.StateArmed || stillPending.StopTargets[0].Stopped() {
		t.Fatalf("stale Stop satisfied barrier = %#v", stillPending)
	}

	observeHook(t, observer, map[string]any{
		"session_id": "target-a", "turn_id": "turn-a-2", "cwd": targetAWorkspace,
		"hook_event_name": "Stop", "stop_hook_active": false,
	})
	complete, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	stopped, unseen := complete.BarrierProgress()
	if complete.State != pluginstate.StateDryRunComplete || stopped != 2 || unseen != 0 ||
		complete.ReasonCode != "after_all_stop_observed_no_action" {
		t.Fatalf("completed barrier = %#v", complete)
	}
	status := state.Status(complete)
	if status.Barrier == nil || status.Barrier.TargetsStopped != 2 || status.Barrier.TargetsPending != 0 ||
		status.Barrier.Targets[0].SessionRef == "target-a" {
		t.Fatalf("redacted barrier status = %#v", status.Barrier)
	}
	logData, err := os.ReadFile(filepath.Join(state.Root(), "events", jobID+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(logData), "target-a") || strings.Contains(string(logData), "turn-a-2") {
		t.Fatalf("barrier event log leaked raw identities: %s", logData)
	}
}

func TestAfterAllStopContinuationTombstoneAndEarlySessionEndFailClosed(t *testing.T) {
	state, service, observer := testComponents(t)
	root := filepath.Dir(state.Root())
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown","trigger_policy":"after_all_stop",
		"target_session_ids":["continued","ended"],
		"acknowledge_stop_without_success":false,"acknowledge_barrier_across_turns":false,
		"delay_seconds":120,"expires_in_seconds":3600,"mode":"dry_run",
		"verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	observeToolAtWorkspace(t, observer, "controller", "controller-turn", "call-arm-continuation", "arm", root, json.RawMessage(`{}`), arm)
	workspace := filepath.Join(root, "continued-repo")
	observeHook(t, observer, map[string]any{
		"session_id": "continued", "turn_id": "turn-1", "cwd": workspace,
		"hook_event_name": "Stop", "stop_hook_active": true,
	})
	observeHook(t, observer, map[string]any{
		"session_id": "continued", "turn_id": "turn-1", "cwd": workspace,
		"hook_event_name": "Stop", "stop_hook_active": false,
	})
	continued, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if continued.StopTargets[0].ContinuationTurnHash == "" || continued.StopTargets[0].Stopped() {
		t.Fatalf("continuation tombstone = %#v", continued.StopTargets[0])
	}
	observeHook(t, observer, map[string]any{
		"session_id": "ended", "hook_event_name": "SessionEnd", "reason": "other",
	})
	expired, err := state.Load(jobID)
	if err != nil {
		t.Fatal(err)
	}
	if expired.State != pluginstate.StateExpired || expired.ReasonCode != "target_session_ended_before_stop" {
		t.Fatalf("early target SessionEnd = %#v", expired)
	}
}

func testComponents(t *testing.T) (*pluginstate.Store, *pluginapi.Service, *Observer) {
	t.Helper()
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := pluginapi.New(state)
	if err != nil {
		t.Fatal(err)
	}
	observer, err := New(state)
	if err != nil {
		t.Fatal(err)
	}
	return state, service, observer
}

func observeTool(t *testing.T, observer *Observer, sessionID, turnID, toolUseID, tool string, input json.RawMessage, result pluginapi.Result) {
	t.Helper()
	observeToolAtWorkspace(t, observer, sessionID, turnID, toolUseID, tool, "", input, result)
}

func observeToolAtWorkspace(t *testing.T, observer *Observer, sessionID, turnID, toolUseID, tool, workspace string, input json.RawMessage, result pluginapi.Result) {
	t.Helper()
	response, err := json.Marshal(map[string]any{
		"content":           []map[string]any{{"type": "text", "text": result.Text}},
		"structuredContent": result.Structured,
		"isError":           result.IsError,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{
		"session_id":      sessionID,
		"turn_id":         turnID,
		"cwd":             workspace,
		"hook_event_name": "PostToolUse",
		"tool_name":       "mcp__done_then__" + tool,
		"tool_use_id":     toolUseID,
		"tool_input":      input,
		"tool_response":   json.RawMessage(response),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.Handle(strings.NewReader(string(payload))); err != nil {
		t.Fatal(err)
	}
}

func observeHook(t *testing.T, observer *Observer, fields map[string]any) {
	t.Helper()
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.Handle(strings.NewReader(string(payload))); err != nil {
		t.Fatal(err)
	}
}
