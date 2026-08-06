package pluginpower

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/hostauthority"
	"github.com/andyandymike/done-then/internal/platform"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/verifierprofile"
)

var errJobChanged = errors.New("plugin job changed during host inspection")
var errAuthorityNotReady = errors.New("host authority has not observed the target yet")
var ErrIntentRecoveryRequired = errors.New("unresolved action intent requires manual cancel or reconcile")
var ErrSupervisorOwnershipUnproven = errors.New("one-shot supervisor process identity does not match the armed job")

type Snapshotter interface {
	Snapshot(ctx context.Context, targetThreadID, cwd string) (hostauthority.Snapshot, error)
}

type SupervisorConfig struct {
	Store                    *pluginstate.Store
	Authority                Snapshotter
	Backend                  actions.Backend
	Profiles                 *verifierprofile.Registry
	AcquireLock              func() (platform.PowerLock, error)
	Now                      func() time.Time
	PollInterval             time.Duration
	Quiescence               time.Duration
	MaxCountdownLateness     time.Duration
	ProcessID                int
	PolicyFingerprint        string
	CurrentPolicyFingerprint func() (string, error)
	UnresolvedPowerJobs      func(currentJobID string) ([]string, error)
}

type Supervisor struct {
	config SupervisorConfig
}

func NewSupervisor(config SupervisorConfig) (*Supervisor, error) {
	if config.Store == nil || config.Backend == nil || config.AcquireLock == nil || config.UnresolvedPowerJobs == nil {
		return nil, errors.New("plugin supervisor requires state, backend, power lock, and power-job inventory")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.Quiescence <= 0 {
		config.Quiescence = 750 * time.Millisecond
	}
	if config.MaxCountdownLateness <= 0 {
		config.MaxCountdownLateness = 30 * time.Second
	}
	if config.MaxCountdownLateness > 5*time.Minute {
		return nil, errors.New("plugin supervisor countdown lateness limit exceeds five minutes")
	}
	if config.ProcessID == 0 {
		config.ProcessID = os.Getpid()
	}
	if config.ProcessID < 1 {
		return nil, errors.New("plugin supervisor process identity is unavailable")
	}
	return &Supervisor{config: config}, nil
}

func (s *Supervisor) Run(ctx context.Context, jobID string) error {
	if err := pluginstate.ValidateJobID(jobID); err != nil {
		return err
	}
	identityDeadline := time.Now().Add(15 * time.Second)
	for {
		job, err := s.config.Store.Load(jobID)
		if err != nil {
			return err
		}
		if job.State.IsTerminal() {
			return nil
		}
		if job.SupervisorPID == 0 {
			if time.Now().Before(identityDeadline) {
				if err := waitContext(ctx, s.config.PollInterval); err != nil {
					return err
				}
				continue
			}
			return ErrSupervisorOwnershipUnproven
		}
		if job.SupervisorPID != s.config.ProcessID {
			return ErrSupervisorOwnershipUnproven
		}
		if job.DryRun {
			return s.fail(job, pluginstate.StateOrphaned, "dry_run_supervisor_not_allowed")
		}
		if pluginstate.HasUnresolvedPowerAction(job) && job.State != pluginstate.StateActionScheduled {
			return ErrIntentRecoveryRequired
		}
		if job.State == pluginstate.StateActionScheduled {
			return s.monitorScheduled(ctx, job)
		}
		if job.TriggerPolicy == pluginstate.TriggerAfterStop {
			if job.Expired(s.config.Now().UTC()) {
				return s.fail(job, pluginstate.StateExpired, "arm_expired")
			}
			if job.State == pluginstate.StateStopObserved {
				if err := s.schedule(ctx, jobID); err != nil {
					if errors.Is(err, errJobChanged) {
						continue
					}
					return err
				}
				continue
			}
			if err := waitContext(ctx, s.config.PollInterval); err != nil {
				return err
			}
			continue
		}
		if !s.verifiedConfigReady() {
			return s.fail(job, pluginstate.StateHostUnavailable, "verified_success_supervisor_configuration_unavailable")
		}
		if !s.policyMatches(job) {
			return s.fail(job, pluginstate.StatePrivilegeUnavailable, "power_policy_changed_after_arm")
		}
		if job.HookFingerprintH1 != "" && job.HostInstanceID == "" {
			return s.fail(job, pluginstate.StateHostUnavailable, "host_instance_identity_missing")
		}
		if job.State != pluginstate.StateActionScheduled && job.Expired(s.config.Now().UTC()) {
			return s.fail(job, pluginstate.StateExpired, "arm_expired")
		}

		if job.SessionID != "" && job.HookFingerprintH1 == "" {
			if err := s.captureH1(ctx, job); err != nil {
				if errors.Is(err, errJobChanged) {
					continue
				}
				if errors.Is(err, errAuthorityNotReady) {
					if waitErr := waitContext(ctx, s.config.PollInterval); waitErr != nil {
						return waitErr
					}
					continue
				}
				return err
			}
			continue
		}
		if job.State == pluginstate.StateReadyPendingStop && job.FinishObserved && job.HookFingerprintH2 == "" {
			if err := s.captureH2(ctx, job); err != nil {
				if errors.Is(err, errJobChanged) {
					continue
				}
				return err
			}
			continue
		}
		if job.State == pluginstate.StateStopObserved {
			if job.HookFingerprintH2 == "" {
				if err := s.captureH2(ctx, job); err != nil {
					if errors.Is(err, errJobChanged) {
						continue
					}
					return err
				}
				continue
			}
			ready, err := s.captureReadyH3(ctx, job)
			if err != nil {
				if errors.Is(err, errJobChanged) {
					continue
				}
				return err
			}
			if ready {
				if err := s.schedule(ctx, jobID); err != nil {
					return err
				}
				continue
			}
		}
		if err := waitContext(ctx, s.config.PollInterval); err != nil {
			return err
		}
	}
}

func (s *Supervisor) captureH1(ctx context.Context, job pluginstate.Job) error {
	snapshot, err := s.config.Authority.Snapshot(ctx, job.SessionID, job.WorkspaceCWD)
	if err != nil {
		return s.fail(job, pluginstate.StateHostUnavailable, "host_snapshot_h1_failed")
	}
	if (!snapshot.SameHostProven || !snapshot.LiveTargetObserved) && s.config.Now().UTC().Before(job.CreatedAt.Add(15*time.Second)) {
		return errAuthorityNotReady
	}
	if state, reason, failed := classifySnapshot(snapshot); failed {
		return s.fail(job, state, reason)
	}
	_, _, err = s.config.Store.UpdateJob(job.JobID, "host.snapshot.h1", "", func(current *pluginstate.Job, _ time.Time) error {
		if current.Generation != job.Generation || current.HookFingerprintH1 != "" || current.SessionID != job.SessionID {
			return errJobChanged
		}
		current.HookFingerprintH1 = snapshot.HookDecision.Fingerprint
		current.HostInstanceID = snapshot.HostInstanceID
		current.HookCompatibility = "compatible"
		current.HostSnapshotReason = "h1_captured"
		if current.State == pluginstate.StateArmed {
			current.State = pluginstate.StateHostMonitoring
			current.ReasonCode = "host_monitoring"
		}
		return nil
	})
	return err
}

func (s *Supervisor) captureH2(ctx context.Context, job pluginstate.Job) error {
	snapshot, err := s.config.Authority.Snapshot(ctx, job.SessionID, job.WorkspaceCWD)
	if err != nil {
		return s.fail(job, pluginstate.StateHostUnavailable, "host_snapshot_h2_failed")
	}
	if state, reason, failed := classifySnapshot(snapshot); failed {
		return s.fail(job, state, reason)
	}
	if snapshot.HookDecision.Fingerprint != job.HookFingerprintH1 {
		return s.fail(job, pluginstate.StateHookConflict, "hook_policy_changed_between_h1_h2")
	}
	if snapshot.HostInstanceID != job.HostInstanceID {
		return s.fail(job, pluginstate.StateHostUnavailable, "host_instance_changed_between_h1_h2")
	}
	_, _, err = s.config.Store.UpdateJob(job.JobID, "host.snapshot.h2", "", func(current *pluginstate.Job, _ time.Time) error {
		if current.Generation != job.Generation || current.HookFingerprintH2 != "" || current.HookFingerprintH1 != job.HookFingerprintH1 {
			return errJobChanged
		}
		current.HookFingerprintH2 = snapshot.HookDecision.Fingerprint
		current.HostSnapshotReason = "h2_captured"
		return nil
	})
	return err
}

func (s *Supervisor) captureReadyH3(ctx context.Context, job pluginstate.Job) (bool, error) {
	snapshot, err := s.config.Authority.Snapshot(ctx, job.SessionID, job.WorkspaceCWD)
	if err != nil {
		return false, s.fail(job, pluginstate.StateHostUnavailable, "host_snapshot_h3_failed")
	}
	if state, reason, failed := classifySnapshot(snapshot); failed {
		return false, s.fail(job, state, reason)
	}
	if snapshot.HookDecision.Fingerprint != job.HookFingerprintH1 || snapshot.HookDecision.Fingerprint != job.HookFingerprintH2 {
		return false, s.fail(job, pluginstate.StateHookConflict, "hook_policy_changed_before_final_gate")
	}
	if snapshot.HostInstanceID != job.HostInstanceID {
		return false, s.fail(job, pluginstate.StateHostUnavailable, "host_instance_changed_before_final_gate")
	}
	if !snapshot.ReadyForFinalGate(job.StopTurnID) {
		return false, nil
	}
	_, _, err = s.config.Store.UpdateJob(job.JobID, "host.snapshot.h3", "", func(current *pluginstate.Job, _ time.Time) error {
		if current.Generation != job.Generation || current.State != pluginstate.StateStopObserved ||
			current.HookFingerprintH1 != job.HookFingerprintH1 || current.HookFingerprintH2 != job.HookFingerprintH2 {
			return errJobChanged
		}
		current.HookFingerprintH3 = snapshot.HookDecision.Fingerprint
		current.HostSnapshotReason = "h3_ready"
		return nil
	})
	return err == nil, err
}

func (s *Supervisor) schedule(ctx context.Context, jobID string) error {
	lock, err := s.config.AcquireLock()
	if err != nil {
		job, loadErr := s.config.Store.Load(jobID)
		if loadErr != nil {
			return loadErr
		}
		return s.fail(job, pluginstate.StateConcurrentConflict, "machine_power_lock_held")
	}
	defer lock.Release()
	unresolved, err := s.config.UnresolvedPowerJobs(jobID)
	if err != nil {
		job, loadErr := s.config.Store.Load(jobID)
		if loadErr != nil {
			return loadErr
		}
		return s.fail(job, pluginstate.StateConcurrentConflict, "unresolved_power_inventory_unavailable")
	}
	if len(unresolved) != 0 {
		job, loadErr := s.config.Store.Load(jobID)
		if loadErr != nil {
			return loadErr
		}
		return s.fail(job, pluginstate.StateConcurrentConflict, "another_power_job_is_unresolved")
	}

	job, err := s.config.Store.Load(jobID)
	if err != nil {
		return err
	}
	if job.State != pluginstate.StateStopObserved {
		return errJobChanged
	}
	if job.TriggerPolicy == pluginstate.TriggerAfterStop {
		if !job.StopWithoutSuccessAck || job.SessionID == "" || job.StopTurnID == "" || job.StopTurnID != job.CurrentTurnID {
			return s.fail(job, pluginstate.StateHookUnavailable, "after_stop_binding_incomplete")
		}
	} else {
		if job.HookFingerprintH3 == "" || job.HookFingerprintH1 != job.HookFingerprintH2 || job.HookFingerprintH2 != job.HookFingerprintH3 {
			return errJobChanged
		}
		if job.PowerPolicyFingerprint != s.config.PolicyFingerprint {
			return s.fail(job, pluginstate.StatePrivilegeUnavailable, "power_policy_changed_before_action")
		}
		if !s.policyMatches(job) {
			return s.fail(job, pluginstate.StatePrivilegeUnavailable, "power_policy_unavailable_before_action")
		}
		if job.VerifierProfile != "none" {
			profile, profileErr := s.config.Profiles.Load(job.VerifierProfile)
			if profileErr != nil || !job.VerifierPassed || profile.Fingerprint != job.VerifierFingerprint {
				return s.fail(job, pluginstate.StateVerificationFailed, "verifier_profile_changed_before_action")
			}
		} else if !job.AllowAgentOnlySuccess {
			return s.fail(job, pluginstate.StateVerificationFailed, "independent_evidence_required")
		}
	}

	if err := waitContext(ctx, s.config.Quiescence); err != nil {
		return err
	}
	current, err := s.config.Store.Load(jobID)
	if err != nil {
		return err
	}
	if current.Generation != job.Generation || current.State != pluginstate.StateStopObserved {
		return errJobChanged
	}
	if current.TriggerPolicy == pluginstate.TriggerVerifiedSuccess {
		finalSnapshot, snapshotErr := s.config.Authority.Snapshot(ctx, current.SessionID, current.WorkspaceCWD)
		if snapshotErr != nil {
			return s.fail(current, pluginstate.StateHostUnavailable, "final_host_snapshot_failed")
		}
		if state, reason, failed := classifySnapshot(finalSnapshot); failed {
			return s.fail(current, state, reason)
		}
		if !finalSnapshot.ReadyForFinalGate(current.StopTurnID) || finalSnapshot.HookDecision.Fingerprint != current.HookFingerprintH3 ||
			finalSnapshot.HostInstanceID != current.HostInstanceID {
			return s.fail(current, pluginstate.StateInventoryPartial, "final_quiescence_gate_changed")
		}
		if !s.policyMatches(current) {
			return s.fail(current, pluginstate.StatePrivilegeUnavailable, "power_policy_changed_at_final_gate")
		}
	}

	now := s.config.Now().UTC()
	delay := time.Duration(current.DelaySeconds) * time.Second
	comment := actions.PluginPowerComment(current.JobID)
	if current.TriggerPolicy == pluginstate.TriggerAfterStop {
		comment = actions.AfterStopPowerComment(current.JobID)
	}
	request := actions.PowerRequest{
		JobID:       current.JobID,
		Action:      current.Action,
		Delay:       delay,
		Comment:     comment,
		RequestedAt: now,
	}
	capabilities, err := s.config.Backend.Preflight(ctx, request)
	if err != nil || !capabilities.ExecuteSupported {
		current.PowerCapabilities = &capabilities
		state := pluginstate.StatePrivilegeUnavailable
		reason := "platform_preflight_failed"
		if errors.Is(err, actions.ErrPlatformUnsupported) {
			state = pluginstate.StatePlatformUnsupported
			reason = "platform_unsupported"
		} else if errors.Is(err, actions.ErrPowerActionConflict) {
			state = pluginstate.StateConcurrentConflict
			reason = "unresolved_power_job_conflict"
		}
		return s.fail(current, state, reason)
	}
	intentReceipt, err := actions.BuildIntentReceipt(current.JobID, current.Action, now, delay, capabilities)
	if err != nil {
		return s.fail(current, pluginstate.StateActionFailed, "intent_recovery_receipt_failed")
	}
	intentDeadline := intentReceipt.Deadline.UTC()
	intentGeneration := current.Generation
	current, _, err = s.config.Store.UpdateJob(current.JobID, "power.intent", "", func(job *pluginstate.Job, _ time.Time) error {
		if job.Generation != intentGeneration || job.State != pluginstate.StateStopObserved {
			return errJobChanged
		}
		job.State = pluginstate.StateActionIntent
		job.ReasonCode = "action_intent_recorded"
		job.ActionIntentAt = &now
		job.PowerCapabilities = &capabilities
		job.PowerReceipt = &intentReceipt
		job.ScheduledFor = &intentDeadline
		return nil
	})
	if err != nil {
		return err
	}

	rechecked, err := s.config.Store.Load(current.JobID)
	if err != nil {
		return err
	}
	if rechecked.State != pluginstate.StateActionIntent {
		return errJobChanged
	}
	if rechecked.CancelRequested {
		return s.cancelPowerForReason(ctx, rechecked, intentReceipt, valueOrCancelReason(rechecked, "continuation_before_schedule"))
	}
	if rechecked.Generation != intentGeneration {
		return errJobChanged
	}
	receipt, err := s.config.Backend.Schedule(ctx, request)
	if err != nil {
		unknown, _, stateErr := s.config.Store.UpdateJob(rechecked.JobID, "power.schedule_unknown", "", func(current *pluginstate.Job, _ time.Time) error {
			if current.State != pluginstate.StateActionIntent {
				return errJobChanged
			}
			current.ReasonCode = "power_schedule_outcome_unknown"
			return nil
		})
		if stateErr != nil {
			return stateErr
		}
		if unknown.CancelRequested {
			cancelErr := s.cancelPowerForReason(ctx, unknown, intentReceipt, valueOrCancelReason(unknown, "cancellation_during_unknown_schedule_outcome"))
			if cancelErr != nil {
				return fmt.Errorf("schedule outcome is unknown and requested cancellation was not confirmed: schedule: %w; cancel: %v", err, cancelErr)
			}
			return nil
		}
		return fmt.Errorf("schedule outcome is unknown; manual cancel or reconcile is required: %w", err)
	}
	if err := actions.ValidateReceiptForRequest(receipt, request, capabilities); err != nil {
		_ = s.cancelAcceptedAction(ctx, rechecked, receipt, "invalid_power_receipt")
		return fmt.Errorf("validate power receipt: %w", err)
	}

	afterSchedule, loadErr := s.config.Store.Load(rechecked.JobID)
	if loadErr == nil && afterSchedule.CancelRequested {
		return s.cancelPowerForReason(ctx, afterSchedule, receipt, valueOrCancelReason(afterSchedule, "continuation_during_schedule"))
	}
	if loadErr != nil || afterSchedule.State != pluginstate.StateActionIntent || afterSchedule.Generation != intentGeneration {
		_ = s.cancelAcceptedAction(ctx, rechecked, receipt, "job_changed_after_schedule")
		if loadErr != nil {
			return loadErr
		}
		return errJobChanged
	}
	scheduledFor := receipt.Deadline.UTC()
	_, _, err = s.config.Store.UpdateJob(rechecked.JobID, "power.scheduled", "", func(job *pluginstate.Job, _ time.Time) error {
		if job.State != pluginstate.StateActionIntent || job.Generation != intentGeneration {
			return errJobChanged
		}
		job.State = pluginstate.StateActionScheduled
		job.ReasonCode = "action_scheduled"
		job.PowerReceipt = &receipt
		job.ScheduledFor = &scheduledFor
		return nil
	})
	if err != nil {
		_ = s.cancelAcceptedAction(ctx, rechecked, receipt, "scheduled_state_persist_failed")
		return err
	}
	return nil
}

func (s *Supervisor) monitorScheduled(ctx context.Context, scheduled pluginstate.Job) error {
	if scheduled.PowerReceipt == nil || scheduled.ScheduledFor == nil {
		return errors.New("scheduled plugin job has no recovery receipt")
	}
	deadline := scheduled.ScheduledFor.UTC()
	for {
		current, err := s.config.Store.Load(scheduled.JobID)
		if err != nil {
			return err
		}
		if current.State.IsTerminal() {
			return nil
		}
		if current.State != pluginstate.StateActionScheduled || current.PowerReceipt == nil || current.PowerReceipt.Checksum != scheduled.PowerReceipt.Checksum {
			return errors.New("scheduled plugin job changed during countdown monitoring")
		}
		now := s.config.Now().UTC()
		reason := current.CancelReason
		if current.CancelRequested && reason == "" {
			reason = "cancellation_requested_during_countdown"
		}
		if !current.CancelRequested && !now.Before(deadline) {
			if now.Sub(deadline) <= s.config.MaxCountdownLateness {
				return nil
			}
			reason = "countdown_deadline_missed_after_sleep_or_stall"
		}
		if !current.CancelRequested && reason == "" && current.TriggerPolicy == pluginstate.TriggerVerifiedSuccess {
			if !s.policyMatches(current) {
				reason = "power_policy_changed_during_countdown"
			} else if current.VerifierProfile != "none" {
				profile, profileErr := s.config.Profiles.Load(current.VerifierProfile)
				if profileErr != nil || !current.VerifierPassed || profile.Fingerprint != current.VerifierFingerprint {
					reason = "verifier_profile_changed_during_countdown"
				}
			}
		}
		if !current.CancelRequested && reason == "" && current.TriggerPolicy == pluginstate.TriggerVerifiedSuccess {
			snapshot, snapshotErr := s.config.Authority.Snapshot(ctx, current.SessionID, current.WorkspaceCWD)
			if snapshotErr != nil {
				reason = "host_unavailable_during_countdown"
			} else if state, classifiedReason, failed := classifySnapshot(snapshot); failed {
				reason = string(state) + ":" + classifiedReason
			} else if !snapshot.ReadyForFinalGate(current.StopTurnID) || snapshot.HookDecision.Fingerprint != current.HookFingerprintH3 ||
				snapshot.HostInstanceID != current.HostInstanceID {
				reason = "host_evidence_changed_during_countdown"
			}
		}
		if reason != "" {
			if err := s.cancelPowerForReason(ctx, current, *current.PowerReceipt, reason); err == nil {
				return nil
			}
			retryDelay := s.config.PollInterval
			if retryDelay < time.Second {
				retryDelay = time.Second
			}
			if err := waitContext(ctx, retryDelay); err != nil {
				cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = s.cancelPowerForReason(cancelCtx, current, *current.PowerReceipt, reason)
				return err
			}
			continue
		}

		wait := s.config.PollInterval
		if remaining := deadline.Sub(s.config.Now().UTC()); remaining < wait {
			wait = remaining
		}
		if err := waitContext(ctx, wait); err != nil {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cancelErr := s.cancelPowerForReason(cancelCtx, current, *current.PowerReceipt, "supervisor_interrupted_during_countdown")
			if cancelErr != nil {
				return fmt.Errorf("%w; emergency countdown cancellation failed: %v", err, cancelErr)
			}
			return err
		}
	}
}

func (s *Supervisor) policyMatches(job pluginstate.Job) bool {
	if s.config.CurrentPolicyFingerprint == nil || job.PowerPolicyFingerprint == "" || job.PowerPolicyFingerprint != s.config.PolicyFingerprint {
		return false
	}
	current, err := s.config.CurrentPolicyFingerprint()
	return err == nil && current == job.PowerPolicyFingerprint
}

func (s *Supervisor) verifiedConfigReady() bool {
	return s.config.Authority != nil && s.config.Profiles != nil && s.config.PolicyFingerprint != "" &&
		s.config.CurrentPolicyFingerprint != nil
}

func (s *Supervisor) cancelPowerForReason(ctx context.Context, job pluginstate.Job, receipt actions.Receipt, reason string) error {
	result, cancelErr := s.config.Backend.Cancel(ctx, receipt)
	_, _, stateErr := s.config.Store.UpdateJob(job.JobID, "power.countdown_cancel", "", func(current *pluginstate.Job, _ time.Time) error {
		if current.State.IsTerminal() {
			return nil
		}
		if !pluginstate.HasUnresolvedPowerAction(*current) {
			return errors.New("plugin power action is no longer unresolved")
		}
		current.CancelRequested = true
		current.CancelReason = reason
		current.CancelResult = &result
		if cancelErr == nil || errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
			current.State = pluginstate.StateCancelled
			current.ReasonCode = "countdown_cancelled:" + reason
		} else {
			current.ReasonCode = "countdown_cancel_unconfirmed:" + reason
		}
		return nil
	})
	if stateErr != nil {
		return stateErr
	}
	if cancelErr != nil && !errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
		return cancelErr
	}
	return nil
}

func valueOrCancelReason(job pluginstate.Job, fallback string) string {
	if job.CancelReason != "" {
		return job.CancelReason
	}
	return fallback
}

func (s *Supervisor) cancelAcceptedAction(ctx context.Context, job pluginstate.Job, receipt actions.Receipt, reason string) error {
	cancelReceipt := receipt
	if actions.ValidateReceipt(cancelReceipt) != nil || cancelReceipt.JobID != job.JobID || cancelReceipt.Action != job.Action {
		if job.PowerReceipt == nil {
			return errors.New("accepted action has no valid recovery receipt")
		}
		cancelReceipt = *job.PowerReceipt
	}
	result, cancelErr := s.config.Backend.Cancel(ctx, cancelReceipt)
	_, _, stateErr := s.config.Store.UpdateJob(job.JobID, "power.rollback", "", func(current *pluginstate.Job, _ time.Time) error {
		current.CancelResult = &result
		if current.State.IsTerminal() {
			return nil
		}
		if cancelErr == nil || errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
			current.State = pluginstate.StateActionFailed
			current.ReasonCode = reason
		} else {
			current.State = pluginstate.StateActionIntent
			current.ReasonCode = reason + "_cancel_unconfirmed"
		}
		return nil
	})
	if cancelErr != nil && !errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
		return cancelErr
	}
	return stateErr
}

func (s *Supervisor) fail(job pluginstate.Job, state pluginstate.State, reason string) error {
	_, _, err := s.config.Store.UpdateJob(job.JobID, "supervisor.fail_closed", "", func(current *pluginstate.Job, _ time.Time) error {
		if current.State.IsTerminal() || current.State == pluginstate.StateActionScheduled {
			return nil
		}
		current.State = state
		current.ReasonCode = reason
		current.HostSnapshotReason = reason
		return nil
	})
	return err
}

func classifySnapshot(snapshot hostauthority.Snapshot) (pluginstate.State, string, bool) {
	if !snapshot.SameHostProven || !snapshot.LiveTargetObserved {
		return pluginstate.StateHostUnavailable, "same_host_not_proven", true
	}
	if !snapshot.InventoryComplete || snapshot.EventLossDetected {
		return pluginstate.StateInventoryPartial, "host_inventory_incomplete", true
	}
	if !snapshot.HookDecision.Compatible || snapshot.HookDecision.Fingerprint == "" {
		return pluginstate.StateHookConflict, "hook_inventory_conflict", true
	}
	return "", "", false
}

func waitContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
