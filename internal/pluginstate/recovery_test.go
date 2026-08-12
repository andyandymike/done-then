package pluginstate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/identity"
)

func TestRecoveryAuthoritySurvivesMutableJobProjection(t *testing.T) {
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
	job := Job{
		SchemaVersion: CurrentSchemaVersion, JobID: jobIdentity.JobID, NonceHash: jobIdentity.NonceHash,
		State: StateStopObserved, ReasonCode: "after_stop_observed_awaiting_countdown", Action: "shutdown",
		TriggerPolicy: TriggerAfterStop, StopWithoutSuccessAck: true, DelaySeconds: 120,
		ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now, Generation: 1,
		SessionID: "recovery-session-secret", ArmTurnID: "turn-1", CurrentTurnID: "turn-1", StopTurnID: "turn-1",
		VerifierProfile: "none", HookCompatibility: "session_bound", ArmObserved: true,
		WorkspaceCWD: filepath.Clean(root),
	}
	if err := state.Create(job); err != nil {
		t.Fatal(err)
	}
	backend := &actions.FakeBackend{}
	request := actions.PowerRequest{
		JobID: job.JobID, Action: job.Action, Delay: 2 * time.Minute,
		Comment: "test recovery", RequestedAt: now,
	}
	capabilities, err := backend.Preflight(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := actions.BuildIntentReceipt(job.JobID, job.Action, now, request.Delay, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := state.PersistRecoveryEnvelope(job, intent, now)
	if err != nil {
		t.Fatal(err)
	}
	call, err := state.PersistScheduleCallStarted(job.JobID, envelope.EnvelopeHash, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.PersistScheduleCallStarted(job.JobID, envelope.EnvelopeHash, now.Add(2*time.Second)); err == nil {
		t.Fatal("a second Schedule call-start marker crossed the no-retry boundary")
	}
	accepted := intent
	accepted.BackendVersion = "test-accepted"
	accepted.ResultCode = 0
	accepted.ResultSummary = "accepted"
	accepted = actions.SealReceipt(accepted)
	seal, err := state.PersistScheduleReceipt(job.JobID, envelope.EnvelopeHash, call.CallHash, accepted, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	authority, err := state.LoadRecoveryAuthority(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !authority.RequiresCancellation() || authority.CancellationReceipt().Checksum != accepted.Checksum ||
		authority.Receipt == nil || authority.Receipt.SealHash != seal.SealHash {
		t.Fatalf("recovery authority = %#v", authority)
	}
	status := authority.Status()
	if status.Phase != "SCHEDULE_RECEIPT_SEALED" || !status.RequiresSettlement ||
		!status.RequiresCancellation || !status.ReceiptSealed {
		t.Fatalf("recovery status = %#v", status)
	}
	recoveryBytes, err := os.ReadFile(state.recoveryEnvelopePath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(recoveryBytes, []byte(job.SessionID)) || bytes.Contains(recoveryBytes, []byte(job.StopTurnID)) {
		t.Fatalf("recovery envelope leaked raw session/turn data: %s", recoveryBytes)
	}
	invalidResult := actions.CancelResult{Scope: capabilities.CancelScope, ResultCode: 0}
	if _, err := state.PersistRecoveryResolution(job.JobID, "cancelled", "missing_inert_evidence", &invalidResult, now.Add(3*time.Second)); err == nil {
		t.Fatal("cancellation without positive inert evidence was accepted")
	}
	result := actions.CancelResult{Cancelled: true, Scope: capabilities.CancelScope, ResultCode: 0, ResultSummary: "cancelled"}
	resolution, err := state.PersistRecoveryResolution(job.JobID, "cancelled", "test_cancel", &result, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	authority, err = state.LoadRecoveryAuthority(job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if authority.RequiresCancellation() || authority.Resolution == nil || authority.Resolution.ResolutionHash != resolution.ResolutionHash {
		t.Fatalf("resolved recovery authority = %#v", authority)
	}
	if status = authority.Status(); status.Phase != "RESOLVED" || status.RequiresSettlement ||
		status.RequiresCancellation || status.Resolution != "cancelled" {
		t.Fatalf("resolved recovery status = %#v", status)
	}
	// Resolution is idempotent even if a duplicate worker supplies a different
	// human reason after the first positive inert proof.
	duplicate, err := state.PersistRecoveryResolution(job.JobID, "cancelled", "duplicate_worker", &result, now.Add(4*time.Second))
	if err != nil || duplicate.ResolutionHash != resolution.ResolutionHash {
		t.Fatalf("duplicate resolution = %#v err=%v", duplicate, err)
	}
}
