package pluginpower

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/hostauthority"
	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/platform"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/verifierprofile"
)

type fixedAuthority struct {
	snapshot hostauthority.Snapshot
}

func (a fixedAuthority) Snapshot(context.Context, string, string) (hostauthority.Snapshot, error) {
	return a.snapshot, nil
}

type sequenceAuthority struct {
	snapshots []hostauthority.Snapshot
	index     int
}

func (a *sequenceAuthority) Snapshot(context.Context, string, string) (hostauthority.Snapshot, error) {
	if len(a.snapshots) == 0 {
		return hostauthority.Snapshot{}, errors.New("no authority snapshots")
	}
	index := a.index
	if index >= len(a.snapshots) {
		index = len(a.snapshots) - 1
	} else {
		a.index++
	}
	return a.snapshots[index], nil
}

type fakeLock struct{}

func (fakeLock) Release() error { return nil }

func TestSupervisorSchedulesOnlyAfterStableReadyHostSnapshots(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	worker := newTestSupervisor(t, root, state, backend, readySnapshot(job, "sha256:hooks"))
	if err := worker.Run(context.Background(), job.JobID); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionScheduled || updated.PowerReceipt == nil || updated.HookFingerprintH3 != "sha256:hooks" {
		t.Fatalf("scheduled job = %#v", updated)
	}
	scheduleCalls, cancelCalls, delay, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 0 || delay != 2*time.Minute {
		t.Fatalf("backend schedule=%d cancel=%d delay=%s", scheduleCalls, cancelCalls, delay)
	}
}

func TestSupervisorFailsClosedWhenHookFingerprintChanges(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	worker := newTestSupervisor(t, root, state, backend, readySnapshot(job, "sha256:changed"))
	if err := worker.Run(context.Background(), job.JobID); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateHookConflict || updated.ReasonCode != "hook_policy_changed_before_final_gate" {
		t.Fatalf("failed-closed job = %#v", updated)
	}
	scheduleCalls, _, _, _ := backend.Snapshot()
	if scheduleCalls != 0 {
		t.Fatalf("hook conflict scheduled %d actions", scheduleCalls)
	}
}

func TestSupervisorRejectsProcessThatDoesNotOwnJob(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	worker := newTestSupervisor(t, root, state, backend, readySnapshot(job, "sha256:hooks"))
	worker.config.ProcessID++
	if err := worker.Run(context.Background(), job.JobID); !errors.Is(err, ErrSupervisorOwnershipUnproven) {
		t.Fatalf("Run() error = %v", err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateStopObserved {
		t.Fatalf("duplicate supervisor mutated job = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 0 || cancelCalls != 0 {
		t.Fatalf("duplicate supervisor reached backend: schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestSupervisorPreservesCancellableIntentWhenScheduleOutcomeIsUnknown(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{ScheduleErr: errors.New("injected helper disconnect")}
	worker := newTestSupervisor(t, root, state, backend, readySnapshot(job, "sha256:hooks"))
	if err := worker.Run(context.Background(), job.JobID); err == nil || !errors.Is(err, backend.ScheduleErr) {
		t.Fatalf("Run() error = %v", err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionIntent || updated.PowerReceipt == nil || updated.PowerReceipt.ResultCode != -1 ||
		updated.ReasonCode != "power_schedule_outcome_unknown" {
		t.Fatalf("recovery intent = %#v", updated)
	}
	receipt, err := pluginstate.RecoveryReceipt(updated)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Cancel(context.Background(), receipt); err != nil {
		t.Fatal(err)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 1 {
		t.Fatalf("backend schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestSupervisorSettlesConcurrentCancellationAfterUnknownScheduleOutcome(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	scheduleErr := errors.New("injected helper disconnect")
	backend := &callbackBackend{FakeBackend: &actions.FakeBackend{ScheduleErr: scheduleErr}}
	backend.afterSchedule = func() {
		_, _, err := state.UpdateJob(job.JobID, "test.cancel_during_schedule", "", func(current *pluginstate.Job, _ time.Time) error {
			current.Generation++
			current.CancelRequested = true
			current.CancelReason = "test_cancelled_during_schedule"
			return nil
		})
		if err != nil {
			t.Fatalf("persist concurrent cancellation: %v", err)
		}
	}
	worker := newTestSupervisor(t, root, state, backend, readySnapshot(job, "sha256:hooks"))
	if err := worker.Run(context.Background(), job.JobID); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateCancelled || !updated.CancelRequested || updated.CancelResult == nil {
		t.Fatalf("settled cancellation = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 1 {
		t.Fatalf("backend schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestSupervisorRollsBackMismatchedReceiptWithPersistedRecoveryHandle(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	badReceipt := actions.SealReceipt(actions.Receipt{
		Platform: "fake", BackendID: "fake", BackendVersion: "1", JobID: "dt_OTHER",
		Action: "shutdown", RequestedAt: time.Now().UTC(), ScheduledAt: time.Now().UTC(),
		Deadline: time.Now().UTC().Add(2 * time.Minute), CancelScope: actions.CancelScopeJob,
	})
	backend := &actions.FakeBackend{Receipt: badReceipt}
	worker := newTestSupervisor(t, root, state, backend, readySnapshot(job, "sha256:hooks"))
	if err := worker.Run(context.Background(), job.JobID); err == nil {
		t.Fatal("mismatched receipt unexpectedly succeeded")
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateActionFailed || updated.CancelResult == nil || updated.PowerReceipt == nil ||
		updated.PowerReceipt.JobID != job.JobID {
		t.Fatalf("rolled-back job = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 1 {
		t.Fatalf("backend schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestSupervisorCancelsScheduledCountdownWhenHostContinues(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	ready := readySnapshot(job, "sha256:hooks")
	continued := ready
	continued.Target.Status = hostauthority.ThreadStatus{Type: "active"}
	continued.LoadedThreads = []hostauthority.Thread{continued.Target}
	profiles, err := verifierprofile.New(root)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Authority: &sequenceAuthority{snapshots: []hostauthority.Snapshot{ready, ready, continued}},
		Backend: backend, Profiles: profiles,
		AcquireLock:              func() (platform.PowerLock, error) { return fakeLock{}, nil },
		PolicyFingerprint:        "sha256:policy",
		CurrentPolicyFingerprint: func() (string, error) { return "sha256:policy", nil },
		UnresolvedPowerJobs:      func(string) ([]string, error) { return nil, nil },
		PollInterval:             time.Millisecond,
		Quiescence:               time.Millisecond,
		ProcessID:                4242,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), job.JobID); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateCancelled || !updated.CancelRequested || updated.CancelResult == nil {
		t.Fatalf("continued job = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 1 {
		t.Fatalf("backend schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestSupervisorCancelsScheduledCountdownWhenPolicyChanges(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	profiles, err := verifierprofile.New(root)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Authority: fixedAuthority{snapshot: readySnapshot(job, "sha256:hooks")},
		Backend: backend, Profiles: profiles,
		AcquireLock:       func() (platform.PowerLock, error) { return fakeLock{}, nil },
		PolicyFingerprint: "sha256:policy",
		CurrentPolicyFingerprint: func() (string, error) {
			scheduleCalls, _, _, _ := backend.Snapshot()
			if scheduleCalls > 0 {
				return "sha256:changed", nil
			}
			return "sha256:policy", nil
		},
		UnresolvedPowerJobs: func(string) ([]string, error) { return nil, nil },
		PollInterval:        time.Millisecond,
		Quiescence:          time.Millisecond,
		ProcessID:           4242,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), job.JobID); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateCancelled || updated.CancelReason != "power_policy_changed_during_countdown" {
		t.Fatalf("changed policy job = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 1 {
		t.Fatalf("backend schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestSupervisorCancelsCountdownAfterExcessiveDeadlineLateness(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	profiles, err := verifierprofile.New(root)
	if err != nil {
		t.Fatal(err)
	}
	baseNow := time.Now().UTC()
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Authority: fixedAuthority{snapshot: readySnapshot(job, "sha256:hooks")},
		Backend: backend, Profiles: profiles,
		AcquireLock:              func() (platform.PowerLock, error) { return fakeLock{}, nil },
		PolicyFingerprint:        "sha256:policy",
		CurrentPolicyFingerprint: func() (string, error) { return "sha256:policy", nil },
		UnresolvedPowerJobs:      func(string) ([]string, error) { return nil, nil },
		PollInterval:             time.Millisecond,
		Quiescence:               time.Millisecond,
		MaxCountdownLateness:     30 * time.Second,
		ProcessID:                4242,
		Now: func() time.Time {
			scheduleCalls, _, _, _ := backend.Snapshot()
			if scheduleCalls > 0 {
				return baseNow.Add(3 * time.Minute)
			}
			return baseNow
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Run(context.Background(), job.JobID); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StateCancelled || updated.CancelReason != "countdown_deadline_missed_after_sleep_or_stall" {
		t.Fatalf("late countdown = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 1 {
		t.Fatalf("backend schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

type callbackBackend struct {
	*actions.FakeBackend
	afterSchedule func()
}

func (b *callbackBackend) Schedule(ctx context.Context, request actions.PowerRequest) (actions.Receipt, error) {
	receipt, err := b.FakeBackend.Schedule(ctx, request)
	if b.afterSchedule != nil {
		b.afterSchedule()
	}
	return receipt, err
}

func stoppedExecuteJob(t *testing.T, root string) (*pluginstate.Store, pluginstate.Job) {
	t.Helper()
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
		SchemaVersion:          pluginstate.CurrentSchemaVersion,
		JobID:                  jobIdentity.JobID,
		NonceHash:              jobIdentity.NonceHash,
		State:                  pluginstate.StateStopObserved,
		ReasonCode:             "test_ready",
		DryRun:                 false,
		Action:                 "shutdown",
		DelaySeconds:           120,
		ExpiresAt:              now.Add(time.Hour),
		CreatedAt:              now,
		UpdatedAt:              now,
		SessionID:              "thread-1",
		ArmTurnID:              "turn-1",
		CurrentTurnID:          "turn-1",
		ReadyTurnID:            "turn-1",
		StopTurnID:             "turn-1",
		Generation:             2,
		CompletionStatus:       "done",
		CompletionEvidenceHash: "sha256:evidence",
		VerifierProfile:        "none",
		AllowAgentOnlySuccess:  true,
		HookCompatibility:      "compatible",
		ArmObserved:            true,
		FinishObserved:         true,
		WorkspaceCWD:           filepath.Clean(root),
		PowerPolicyFingerprint: "sha256:policy",
		HookFingerprintH1:      "sha256:hooks",
		HookFingerprintH2:      "sha256:hooks",
		HostInstanceID:         "host-test",
		SupervisorPID:          4242,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	return state, job
}

func readySnapshot(job pluginstate.Job, fingerprint string) hostauthority.Snapshot {
	target := hostauthority.Thread{
		ID:     job.SessionID,
		Status: hostauthority.ThreadStatus{Type: "idle"},
		Turns:  []hostauthority.Turn{{ID: job.StopTurnID, Status: "completed"}},
	}
	return hostauthority.Snapshot{
		Target:             target,
		LoadedThreads:      []hostauthority.Thread{target},
		SameHostProven:     true,
		LiveTargetObserved: true,
		CompletedTurnIDs:   []string{job.StopTurnID},
		InventoryComplete:  true,
		HostInstanceID:     "host-test",
		HookDecision: hostauthority.HookDecision{
			Compatible:  true,
			Fingerprint: fingerprint,
		},
	}
}

func newTestSupervisor(t *testing.T, root string, state *pluginstate.Store, backend actions.Backend, snapshot hostauthority.Snapshot) *Supervisor {
	t.Helper()
	profiles, err := verifierprofile.New(root)
	if err != nil {
		t.Fatal(err)
	}
	baseNow := time.Now().UTC()
	now := func() time.Time {
		if fake, ok := backend.(*actions.FakeBackend); ok {
			scheduleCalls, _, _, _ := fake.Snapshot()
			if scheduleCalls > 0 {
				return baseNow.Add(2*time.Minute + time.Second)
			}
		}
		return baseNow
	}
	worker, err := NewSupervisor(SupervisorConfig{
		Store:                    state,
		Authority:                fixedAuthority{snapshot: snapshot},
		Backend:                  backend,
		Profiles:                 profiles,
		AcquireLock:              func() (platform.PowerLock, error) { return fakeLock{}, nil },
		PolicyFingerprint:        "sha256:policy",
		CurrentPolicyFingerprint: func() (string, error) { return "sha256:policy", nil },
		UnresolvedPowerJobs:      func(string) ([]string, error) { return nil, nil },
		PollInterval:             time.Millisecond,
		Quiescence:               time.Millisecond,
		ProcessID:                4242,
		Now:                      now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}
