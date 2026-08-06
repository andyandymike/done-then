package pluginstate

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/identity"
)

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
