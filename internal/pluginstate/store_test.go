package pluginstate

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/identity"
)

func TestBarrierReservationsSerializeConcurrentStopsAndRevalidateEveryIndex(t *testing.T) {
	root := t.TempDir()
	state, err := New(root)
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
	targetIDs := []string{"target-a", "target-b", "target-c"}
	targets := make([]StopTarget, 0, len(targetIDs))
	for _, sessionID := range targetIDs {
		targets = append(targets, StopTarget{SessionHash: identity.SHA256([]byte(sessionID))})
	}
	now := time.Now().UTC()
	job := Job{
		SchemaVersion: CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: StateArmPendingBind, ReasonCode: "awaiting_post_tool_hook", Action: "shutdown",
		TriggerPolicy: TriggerAfterAllStop, StopWithoutSuccessAck: true, BarrierAcrossTurnsAck: true,
		DelaySeconds: 120, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		Generation: 1, VerifierProfile: "none", HookCompatibility: "not_evaluated",
		WorkspaceCWD: root, TargetBindingID: bindingIdentity.JobID, StopTargets: targets,
	}
	job, err = state.CreateBarrierReservations(job, targetIDs)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err = state.BindSession(job.JobID, "controller", "controller-turn", root, fmt.Sprintf("%064x", 1))
	if err != nil {
		t.Fatal(err)
	}
	if job.State != StateArmed || !job.TargetIndexesReady {
		t.Fatalf("bound barrier = %#v", job)
	}

	var wait sync.WaitGroup
	errorsChannel := make(chan error, len(targetIDs))
	for index, sessionID := range targetIDs {
		index, sessionID := index, sessionID
		wait.Add(1)
		go func() {
			defer wait.Done()
			eventKey := fmt.Sprintf("%064x", index+10)
			var updateErr error
			for attempt := 0; attempt < 4; attempt++ {
				_, _, _, updateErr = state.UpdateObservedSession(sessionID, "turn", "test.barrier.stop", eventKey, func(job *Job, target *StopTarget, observedAt time.Time) error {
					turnHash := identity.SHA256([]byte("turn"))
					firstSeen := observedAt
					target.WorkspaceCWD = root
					target.FirstSeenAt = &firstSeen
					target.CurrentTurnHash = turnHash
					target.StopTurnHash = turnHash
					target.StopObservedAt = &observedAt
					job.Generation++
					if job.BarrierSatisfied() {
						job.State = StateStopObserved
						job.ReasonCode = "after_all_stop_observed_awaiting_countdown"
					} else {
						job.State = StateArmed
						job.ReasonCode = "after_all_stop_barrier_partial"
					}
					return nil
				})
				if !errors.Is(updateErr, ErrLockTimeout) {
					break
				}
			}
			if updateErr != nil {
				errorsChannel <- updateErr
			}
		}()
	}
	wait.Wait()
	close(errorsChannel)
	for updateErr := range errorsChannel {
		t.Fatal(updateErr)
	}
	stopped, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	count, unseen := stopped.BarrierProgress()
	if stopped.State != StateStopObserved || count != len(targetIDs) || unseen != 0 {
		t.Fatalf("concurrent barrier = %#v", stopped)
	}
	if _, err := state.BarrierAuthority(job.JobID); err != nil {
		t.Fatalf("complete barrier authority = %v", err)
	}
	if err := os.Remove(state.sessionPath(targetIDs[0])); err != nil {
		t.Fatal(err)
	}
	_, err = state.BarrierAuthority(job.JobID)
	var authorityErr *BarrierAuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Reason != "target_index_changed" {
		t.Fatalf("missing live index authority error = %v", err)
	}
}

func TestCapturedEmptyBindingCannotBeAppliedToConcurrentBarrier(t *testing.T) {
	root := t.TempDir()
	state, err := New(root)
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
	targetIDs := []string{"late-target-a", "late-target-b"}
	now := time.Now().UTC()
	job := Job{
		SchemaVersion: CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: StateArmPendingBind, ReasonCode: "awaiting_post_tool_hook", Action: "shutdown",
		TriggerPolicy: TriggerAfterAllStop, StopWithoutSuccessAck: true, BarrierAcrossTurnsAck: true,
		DelaySeconds: 120, ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now,
		Generation: 1, VerifierProfile: "none", HookCompatibility: "not_evaluated",
		WorkspaceCWD: root, TargetBindingID: bindingIdentity.JobID,
		StopTargets: []StopTarget{
			{SessionHash: identity.SHA256([]byte(targetIDs[0]))},
			{SessionHash: identity.SHA256([]byte(targetIDs[1]))},
		},
	}
	job, err = state.CreateBarrierReservations(job, targetIDs)
	if err != nil {
		t.Fatal(err)
	}
	job, _, err = state.BindSession(job.JobID, "late-controller", "controller-turn", root, fmt.Sprintf("%064x", 90))
	if err != nil {
		t.Fatal(err)
	}
	mutated := false
	_, changed, found, err := state.UpdateObservedSessionBinding(
		targetIDs[0], "old-turn", "hook.stop", fmt.Sprintf("%064x", 91), "", "",
		func(*Job, *StopTarget, time.Time) error {
			mutated = true
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !found || mutated {
		t.Fatalf("pre-arm event was applied to a concurrent barrier: changed=%t found=%t mutated=%t", changed, found, mutated)
	}
	loaded, err := state.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.StopTargets[0].FirstSeenAt != nil || loaded.StopTargets[0].CurrentTurnHash != "" {
		t.Fatalf("concurrent barrier target was mutated: %#v", loaded.StopTargets[0])
	}
}

func TestBarrierTargetStopBeforeControllerPostToolUseIsNotCredited(t *testing.T) {
	root := t.TempDir()
	state, err := New(root)
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
	targetIDs := []string{"prebind-target-a", "prebind-target-b"}
	now := time.Now().UTC()
	job := Job{
		SchemaVersion: CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: StateArmPendingBind, ReasonCode: "awaiting_post_tool_hook", Action: "shutdown",
		TriggerPolicy: TriggerAfterAllStop, DelaySeconds: 120,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Generation: 1,
		VerifierProfile: "none", HookCompatibility: "not_evaluated", WorkspaceCWD: root,
		TargetBindingID: bindingIdentity.JobID,
		StopTargets: []StopTarget{
			{SessionHash: identity.SHA256([]byte(targetIDs[0]))},
			{SessionHash: identity.SHA256([]byte(targetIDs[1]))},
		},
	}
	job, err = state.CreateBarrierReservations(job, targetIDs)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := state.LookupObservedSession(targetIDs[0]); err != nil || found {
		t.Fatalf("pending target reservation was exposed as an observed binding: found=%t err=%v", found, err)
	}
	mutated := false
	_, changed, found, err := state.UpdateObservedSessionBinding(
		targetIDs[0], "prebind-turn", "hook.stop", fmt.Sprintf("%064x", 92), job.JobID, job.TargetBindingID,
		func(*Job, *StopTarget, time.Time) error {
			mutated = true
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !found || mutated {
		t.Fatalf("pre-controller Stop was credited: changed=%t found=%t mutated=%t", changed, found, mutated)
	}
	bound, _, err := state.BindSession(job.JobID, "prebind-controller", "controller-turn", root, fmt.Sprintf("%064x", 93))
	if err != nil {
		t.Fatal(err)
	}
	if bound.State != StateArmed || bound.StopTargets[0].CurrentTurnHash != "" || bound.StopTargets[0].FirstSeenAt != nil {
		t.Fatalf("controller binding retained a pre-bind target event: %#v", bound)
	}
	if _, _, found, err := state.LookupObservedSession(targetIDs[0]); err != nil || !found {
		t.Fatalf("bound target was not exposed to Hook observation: found=%t err=%v", found, err)
	}
}

func TestConcurrentProcessesUseSerializedAtomicUpdates(t *testing.T) {
	root := t.TempDir()
	first, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	job := Job{
		SchemaVersion:     "1",
		JobID:             jobIdentity.JobID,
		NonceHash:         jobIdentity.NonceHash,
		State:             StateArmPendingBind,
		ReasonCode:        "test",
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
	if err := first.Create(job); err != nil {
		t.Fatal(err)
	}

	const updates = 12
	var wait sync.WaitGroup
	var successful atomic.Int64
	errorsChannel := make(chan error, updates)
	for index := 0; index < updates; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			selected := first
			if index%2 != 0 {
				selected = second
			}
			eventKey := fmt.Sprintf("%064x", index+1)
			_, _, err := selected.UpdateJob(job.JobID, "test.concurrent", eventKey, func(job *Job, _ time.Time) error {
				job.Generation++
				return nil
			})
			if err == nil {
				successful.Add(1)
				return
			}
			if !errors.Is(err, ErrLockTimeout) {
				errorsChannel <- err
			}
		}(index)
	}
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Fatal(err)
		}
	}
	updated, err := first.Load(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SchemaVersion != CurrentSchemaVersion || updated.TriggerPolicy != TriggerVerifiedSuccess {
		t.Fatalf("legacy job migration = schema %q trigger %q", updated.SchemaVersion, updated.TriggerPolicy)
	}
	committed := int(successful.Load())
	if committed == 0 {
		t.Fatal("all concurrent updates timed out")
	}
	if updated.Generation != uint64(1+committed) {
		t.Fatalf("generation = %d, want %d", updated.Generation, 1+committed)
	}
	if len(updated.ProcessedEventKeys) != committed {
		t.Fatalf("event keys = %d, want %d", len(updated.ProcessedEventKeys), committed)
	}
}

func TestUnresolvedIntentDoesNotExpireAndKeepsRecoveryReceipt(t *testing.T) {
	root := t.TempDir()
	state, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	jobIdentity, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	capabilities := actions.Capabilities{
		Platform: "linux-systemd", BackendID: "linux-systemd-helper", ExecuteSupported: true,
		CancelScope: actions.CancelScopeJob, MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour,
	}
	receipt, err := actions.BuildIntentReceipt(jobIdentity.JobID, "shutdown", now, 2*time.Minute, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	deadline := receipt.Deadline
	job := Job{
		SchemaVersion: CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: StateActionIntent, ReasonCode: "action_intent_recorded", Action: "shutdown", DelaySeconds: 120,
		TriggerPolicy: TriggerVerifiedSuccess,
		ExpiresAt:     now.Add(time.Minute), CreatedAt: now, UpdatedAt: now, SessionID: "thread-1", ArmTurnID: "turn-1",
		Generation: 1, VerifierProfile: "none", AllowAgentOnlySuccess: true, HookCompatibility: "compatible",
		ArmObserved: true, WorkspaceCWD: root, PowerPolicyFingerprint: "sha256:policy", ActionIntentAt: &now,
		ScheduledFor: &deadline, PowerCapabilities: &capabilities, PowerReceipt: &receipt,
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	state.now = func() time.Time { return now.Add(2 * time.Hour) }
	refreshed, err := state.RefreshExpiry(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.State != StateActionIntent || !HasUnresolvedPowerAction(refreshed) {
		t.Fatalf("expired unresolved intent = %#v", refreshed)
	}
	recovered, err := RecoveryReceipt(refreshed)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Checksum != receipt.Checksum || recovered.ExternalToken != actions.SystemdUnitToken(job.JobID) {
		t.Fatalf("recovery receipt = %#v", recovered)
	}
}
