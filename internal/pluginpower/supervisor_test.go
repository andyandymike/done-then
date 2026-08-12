package pluginpower

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

type fixedStopArbitration struct{ err error }

func (a fixedStopArbitration) ValidateFinalStop(context.Context, pluginstate.Job) error {
	return a.err
}

type fixedVerifiedAuthorization struct{ err error }

func (a fixedVerifiedAuthorization) ValidateVerifiedSuccess(context.Context, pluginstate.Job) error {
	return a.err
}

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

func TestAfterStopSupervisorSchedulesWithTrustedFinalArbitration(t *testing.T) {
	root := t.TempDir()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	baseNow := time.Now().UTC()
	job := pluginstate.Job{
		SchemaVersion: pluginstate.CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: pluginstate.StateStopObserved, ReasonCode: "after_stop_observed_awaiting_countdown",
		Action: "shutdown", TriggerPolicy: pluginstate.TriggerAfterStop, StopWithoutSuccessAck: true,
		DelaySeconds: 120, ExpiresAt: baseNow.Add(time.Hour), CreatedAt: baseNow, UpdatedAt: baseNow,
		SessionID: "thread-after-stop", ArmTurnID: "turn-1", CurrentTurnID: "turn-1", StopTurnID: "turn-1",
		Generation: 1, VerifierProfile: "none", HookCompatibility: "session_bound", ArmObserved: true,
		WorkspaceCWD: filepath.Clean(root), SupervisorPID: 4242,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Backend: backend, StopArbitration: fixedStopArbitration{},
		AcquireLock:         func() (platform.PowerLock, error) { return fakeLock{}, nil },
		UnresolvedPowerJobs: func(string) ([]string, error) { return nil, nil },
		PollInterval:        time.Millisecond,
		Quiescence:          time.Millisecond,
		ProcessID:           4242,
		Now: func() time.Time {
			scheduleCalls, _, _, _ := backend.Snapshot()
			if scheduleCalls > 0 {
				return baseNow.Add(2*time.Minute + time.Second)
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
	if updated.State != pluginstate.StateActionScheduled || updated.PowerReceipt == nil ||
		updated.CompletionEvidenceHash != "" || updated.HostInstanceID != "" {
		t.Fatalf("after-stop scheduled job = %#v", updated)
	}
	scheduleCalls, cancelCalls, delay, comment := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 0 || delay != 2*time.Minute || !strings.Contains(comment, "Codex stopped") {
		t.Fatalf("backend schedule=%d cancel=%d delay=%s comment=%q", scheduleCalls, cancelCalls, delay, comment)
	}
}

func TestAfterStopSupervisorFailsClosedWithoutFinalArbitration(t *testing.T) {
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
		State: pluginstate.StateStopObserved, ReasonCode: "after_stop_observed_awaiting_countdown",
		Action: "shutdown", TriggerPolicy: pluginstate.TriggerAfterStop, StopWithoutSuccessAck: true,
		DelaySeconds: 120, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		SessionID: "thread-no-arbitration", ArmTurnID: "turn-1", CurrentTurnID: "turn-1", StopTurnID: "turn-1",
		Generation: 1, VerifierProfile: "none", HookCompatibility: "session_bound", ArmObserved: true,
		WorkspaceCWD: filepath.Clean(root), SupervisorPID: 4242,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Backend: backend,
		AcquireLock:         func() (platform.PowerLock, error) { return fakeLock{}, nil },
		UnresolvedPowerJobs: func(string) ([]string, error) { return nil, nil },
		PollInterval:        time.Millisecond, Quiescence: time.Millisecond, ProcessID: 4242,
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
	if updated.State != pluginstate.StateHookUnavailable || updated.ReasonCode != "stop_arbitration_unavailable" {
		t.Fatalf("missing arbitration job = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 0 || cancelCalls != 0 {
		t.Fatalf("missing arbitration reached backend: schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestVerifiedSuccessSupervisorFailsClosedWithoutIndependentExecutionGrant(t *testing.T) {
	root := t.TempDir()
	state, job := stoppedExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	worker := newTestSupervisor(t, root, state, backend, readySnapshot(job, "sha256:hooks"))
	worker.config.VerifiedAuthorization = nil
	if err := worker.Run(context.Background(), job.JobID); err != nil {
		t.Fatal(err)
	}
	updated, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != pluginstate.StatePrivilegeUnavailable || updated.ReasonCode != "verified_success_authority_unavailable" {
		t.Fatalf("missing verified-success grant job = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 0 || cancelCalls != 0 {
		t.Fatalf("missing verified-success grant reached backend: schedule=%d cancel=%d", scheduleCalls, cancelCalls)
	}
}

func TestAfterAllStopSupervisorSchedulesExactlyOnceWithTrustedFinalArbitration(t *testing.T) {
	root := t.TempDir()
	state, job, targetIDs := stoppedBarrierExecuteJob(t, root)
	backend := &actions.FakeBackend{}
	baseNow := time.Now().UTC()
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Backend: backend, StopArbitration: fixedStopArbitration{},
		AcquireLock:         func() (platform.PowerLock, error) { return fakeLock{}, nil },
		UnresolvedPowerJobs: func(string) ([]string, error) { return nil, nil },
		PollInterval:        time.Millisecond, Quiescence: time.Millisecond, ProcessID: 4242,
		Now: func() time.Time {
			scheduleCalls, _, _, _ := backend.Snapshot()
			if scheduleCalls > 0 {
				return baseNow.Add(2*time.Minute + time.Second)
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
	if updated.State != pluginstate.StateActionScheduled || updated.PowerReceipt == nil {
		t.Fatalf("scheduled barrier = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 0 {
		t.Fatalf("barrier backend schedule=%d cancel=%d targets=%v", scheduleCalls, cancelCalls, targetIDs)
	}
}

func TestAfterAllStopSupervisorRejectsChangedTargetIndex(t *testing.T) {
	root := t.TempDir()
	state, job, targetIDs := stoppedBarrierExecuteJob(t, root)
	indexPath := filepath.Join(state.Root(), "sessions", identity.SHA256([]byte(targetIDs[0]))+".json")
	if err := os.Remove(indexPath); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Backend: backend, StopArbitration: fixedStopArbitration{},
		AcquireLock:         func() (platform.PowerLock, error) { return fakeLock{}, nil },
		UnresolvedPowerJobs: func(string) ([]string, error) { return nil, nil },
		PollInterval:        time.Millisecond, Quiescence: time.Millisecond, ProcessID: 4242,
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
	if updated.State != pluginstate.StateHookUnavailable || updated.ReasonCode != "target_index_changed" {
		t.Fatalf("changed target index = %#v", updated)
	}
	scheduleCalls, _, _, _ := backend.Snapshot()
	if scheduleCalls != 0 {
		t.Fatalf("changed target index scheduled %d actions", scheduleCalls)
	}
}

func TestAfterAllStopResumeDuringScheduleCancelsReceiptBoundAction(t *testing.T) {
	root := t.TempDir()
	state, job, targetIDs := stoppedBarrierExecuteJob(t, root)
	backend := &callbackBackend{FakeBackend: &actions.FakeBackend{}}
	backend.afterSchedule = func() {
		_, _, _, err := state.UpdateObservedSession(targetIDs[0], "turn-2", "test.target_resumed", strings.Repeat("d", 64), func(current *pluginstate.Job, target *pluginstate.StopTarget, _ time.Time) error {
			current.Generation++
			target.CurrentTurnHash = identity.SHA256([]byte("turn-2"))
			target.StopTurnHash = ""
			target.StopObservedAt = nil
			current.CancelRequested = true
			current.CancelReason = "after_all_stop_target_resumed"
			current.ReasonCode = "after_all_stop_target_resumed"
			return nil
		})
		if err != nil {
			t.Fatalf("resume barrier target: %v", err)
		}
	}
	worker, err := NewSupervisor(SupervisorConfig{
		Store: state, Backend: backend, StopArbitration: fixedStopArbitration{},
		AcquireLock:         func() (platform.PowerLock, error) { return fakeLock{}, nil },
		UnresolvedPowerJobs: func(string) ([]string, error) { return nil, nil },
		PollInterval:        time.Millisecond, Quiescence: time.Millisecond, ProcessID: 4242,
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
	if updated.State != pluginstate.StateCancelled || updated.CancelResult == nil ||
		!strings.HasPrefix(updated.ReasonCode, "countdown_cancelled:") {
		t.Fatalf("resumed barrier cancellation = %#v", updated)
	}
	scheduleCalls, cancelCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || cancelCalls != 1 {
		t.Fatalf("resumed barrier backend schedule=%d cancel=%d", scheduleCalls, cancelCalls)
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
		Backend: backend, Profiles: profiles, VerifiedAuthorization: fixedVerifiedAuthorization{},
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
		Backend: backend, Profiles: profiles, VerifiedAuthorization: fixedVerifiedAuthorization{},
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
		Backend: backend, Profiles: profiles, VerifiedAuthorization: fixedVerifiedAuthorization{},
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
		TriggerPolicy:          pluginstate.TriggerVerifiedSuccess,
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

func stoppedBarrierExecuteJob(t *testing.T, root string) (*pluginstate.Store, pluginstate.Job, []string) {
	t.Helper()
	state, err := pluginstate.New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	bindingIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	targetIDs := []string{"barrier-target-a", "barrier-target-b", "barrier-target-c"}
	targets := make([]pluginstate.StopTarget, 0, len(targetIDs))
	for _, sessionID := range targetIDs {
		targets = append(targets, pluginstate.StopTarget{SessionHash: identity.SHA256([]byte(sessionID))})
	}
	now := time.Now().UTC()
	job := pluginstate.Job{
		SchemaVersion: pluginstate.CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: pluginstate.StateArmPendingBind, ReasonCode: "awaiting_post_tool_hook", Action: "shutdown",
		TriggerPolicy: pluginstate.TriggerAfterAllStop, StopWithoutSuccessAck: true, BarrierAcrossTurnsAck: true,
		DelaySeconds: 120, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		Generation: 1, VerifierProfile: "none", HookCompatibility: "not_evaluated", WorkspaceCWD: root,
		SupervisorPID: 4242, TargetBindingID: bindingIdentity.JobID, StopTargets: targets,
	}
	job, err = state.CreateBarrierReservations(job, targetIDs)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err = state.BindSession(job.JobID, "barrier-controller", "controller-turn", root, strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	for index, sessionID := range targetIDs {
		eventKey := strings.Repeat(string(rune('b'+index)), 64)
		job, _, _, err = state.UpdateObservedSession(sessionID, "turn-1", "test.barrier.stop", eventKey, func(current *pluginstate.Job, target *pluginstate.StopTarget, observedAt time.Time) error {
			turnHash := identity.SHA256([]byte("turn-1"))
			firstSeen := observedAt
			target.WorkspaceCWD = root
			target.FirstSeenAt = &firstSeen
			target.CurrentTurnHash = turnHash
			target.StopTurnHash = turnHash
			target.StopObservedAt = &observedAt
			current.Generation++
			if current.BarrierSatisfied() {
				current.State = pluginstate.StateStopObserved
				current.ReasonCode = "after_all_stop_observed_awaiting_countdown"
			} else {
				current.State = pluginstate.StateArmed
				current.ReasonCode = "after_all_stop_barrier_partial"
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if job.State != pluginstate.StateStopObserved {
		t.Fatalf("stopped barrier fixture = %#v", job)
	}
	return state, job, targetIDs
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
		VerifiedAuthorization:    fixedVerifiedAuthorization{},
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
