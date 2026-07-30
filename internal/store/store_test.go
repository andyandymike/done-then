package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/supervisor"
)

func TestStoreCreateSaveLoadAndCancel(t *testing.T) {
	jobStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := testJob("dt_STORETEST123")
	if err := jobStore.Create(job); err != nil {
		t.Fatal(err)
	}
	loaded, err := jobStore.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.JobID != job.JobID || loaded.State != supervisor.StateArmed {
		t.Fatalf("Load() = %#v", loaded)
	}
	loaded.State = supervisor.StateAgentRunning
	loaded.ReasonCode = "agent_started"
	if err := jobStore.Save(loaded); err != nil {
		t.Fatal(err)
	}
	if err := jobStore.RequestCancel(job.JobID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := jobStore.Cancelled(job.JobID)
	if err != nil || !cancelled {
		t.Fatalf("Cancelled() = %v, %v", cancelled, err)
	}
}

func TestStoreRecoverPreActionDoesNotTouchPowerIntent(t *testing.T) {
	jobStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	preAction := testJob("dt_PREACTION123")
	preAction.State = supervisor.StateAgentRunning
	intent := testJob("dt_INTENTTEST123")
	intent.State = supervisor.StateActionIntentRecorded
	intentAt := intent.UpdatedAt
	intent.ActionIntentAt = &intentAt
	if err := jobStore.Create(preAction); err != nil {
		t.Fatal(err)
	}
	if err := jobStore.Create(intent); err != nil {
		t.Fatal(err)
	}
	if err := jobStore.RecoverPreActionJobs(); err != nil {
		t.Fatal(err)
	}
	recovered, _ := jobStore.Load(preAction.JobID)
	kept, _ := jobStore.Load(intent.JobID)
	if recovered.State != supervisor.StateOrphaned {
		t.Fatalf("pre-action state = %s", recovered.State)
	}
	if kept.State != supervisor.StateActionIntentRecorded {
		t.Fatalf("intent state = %s", kept.State)
	}
	active, err := jobStore.ActivePowerJobs()
	if err != nil || len(active) != 1 || active[0].JobID != intent.JobID {
		t.Fatalf("ActivePowerJobs() = %#v, %v", active, err)
	}
}

func TestStoreRejectsCorruptAndTraversalRecords(t *testing.T) {
	jobStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobStore.Load(`..\outside`); err == nil {
		t.Fatal("Load() accepted a traversal job id")
	}
	path := filepath.Join(jobStore.jobsDir(), "dt_CORRUPT123.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":"1"} trailing`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := jobStore.Load("dt_CORRUPT123"); err == nil {
		t.Fatal("Load() accepted a corrupt record")
	}
	invalidState := testJob("dt_BADSTATE123")
	invalidState.State = supervisor.State("POWER_MAYBE")
	if err := atomicWriteJSON(jobStore.jobPath(invalidState.JobID), invalidState); err != nil {
		t.Fatal(err)
	}
	if _, err := jobStore.Load(invalidState.JobID); err == nil {
		t.Fatal("Load() accepted an unknown state")
	}
}

func TestAppendEventContainsNoPromptOrTranscriptFields(t *testing.T) {
	jobStore, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	job := testJob("dt_LOGTEST123")
	if err := jobStore.Create(job); err != nil {
		t.Fatal(err)
	}
	event := supervisor.Event{
		Timestamp: time.Now().UTC(),
		JobID:     job.JobID,
		OldState:  supervisor.StateArmed,
		NewState:  supervisor.StateAgentRunning,
		Reason:    "agent_started",
	}
	if err := jobStore.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jobStore.logPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	value := strings.ToLower(string(data))
	if strings.Contains(value, "prompt") || strings.Contains(value, "transcript") {
		t.Fatalf("sensitive field name in log: %s", data)
	}
}

func testJob(jobID string) supervisor.Job {
	now := time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC)
	return supervisor.Job{
		SchemaVersion: "1",
		JobID:         jobID,
		NonceHash:     "sha256:test",
		State:         supervisor.StateArmed,
		ReasonCode:    "job_armed",
		DryRun:        false,
		Action:        "shutdown",
		DelaySeconds:  120,
		CreatedAt:     now,
		UpdatedAt:     now,
		SupervisorPID: 1,
		CodexCWD:      `D:\Code\project`,
		PromptSHA256:  "hash",
	}
}
