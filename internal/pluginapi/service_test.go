package pluginapi

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/andyandymike/done-then/internal/pluginstate"
)

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
		"mode":"execute","verifier_profile":"none","allow_agent_only_success":true
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
		"mode":"dry_run","verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	if _, _, err := state.BindSession(jobID, "session", "turn", eventKeyForTest("a")); err != nil {
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
		"mode":"dry_run","verifier_profile":"none","allow_agent_only_success":false
	}`))
	jobID := arm.Structured["job_id"].(string)
	if _, _, err := state.BindSession(jobID, "session", "turn", eventKeyForTest("b")); err != nil {
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

func eventKeyForTest(seed string) string {
	value := ""
	for len(value) < 64 {
		value += seed
	}
	return value[:64]
}
