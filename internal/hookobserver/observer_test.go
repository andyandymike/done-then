package hookobserver

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/andyandymike/done-then/internal/pluginapi"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

func TestObserveOnlyLifecycleBindsFinishesAndRecordsStop(t *testing.T) {
	state, service, observer := testComponents(t)
	arm := service.Call(context.Background(), "arm", json.RawMessage(`{
		"action":"shutdown",
		"delay_seconds":120,
		"expires_in_seconds":3600,
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
