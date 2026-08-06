package pluginstate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
)

const CurrentSchemaVersion = "3"

type TriggerPolicy string

const (
	TriggerAfterStop       TriggerPolicy = "after_stop"
	TriggerVerifiedSuccess TriggerPolicy = "verified_success"
)

func (p TriggerPolicy) Valid() bool {
	return p == TriggerAfterStop || p == TriggerVerifiedSuccess
}

type State string

const (
	StateArmPendingBind            State = "ARM_PENDING_BIND"
	StateArmed                     State = "ARMED"
	StateWaiting                   State = "WAITING"
	StateVerifying                 State = "VERIFYING"
	StateReadyPendingStop          State = "READY_PENDING_STOP"
	StateStopObserved              State = "STOP_OBSERVED"
	StateHostMonitoring            State = "HOST_MONITORING"
	StateActionIntent              State = "ACTION_INTENT_RECORDED"
	StateActionScheduled           State = "ACTION_SCHEDULED"
	StateDryRunComplete            State = "DRY_RUN_COMPLETE"
	StateHookConflict              State = "HOOK_CONFLICT"
	StateHookUnavailable           State = "HOOK_UNAVAILABLE"
	StateNotDone                   State = "NOT_DONE"
	StateVerificationFailed        State = "VERIFICATION_FAILED"
	StateExpired                   State = "EXPIRED"
	StateCancelled                 State = "CANCELLED"
	StateOrphaned                  State = "ORPHANED"
	StateHostUnavailable           State = "HOST_AUTHORITY_UNAVAILABLE"
	StateInventoryPartial          State = "TASK_INVENTORY_PARTIAL"
	StateConcurrentConflict        State = "CONCURRENT_TASK_CONFLICT"
	StatePlatformUnsupported       State = "PLATFORM_UNSUPPORTED"
	StatePrivilegeUnavailable      State = "PRIVILEGE_UNAVAILABLE"
	StateActionFailed              State = "ACTION_FAILED"
	StateActionExecutionUnverified State = "ACTION_EXECUTION_UNVERIFIED"
	StateActionExecutedConfirmed   State = "ACTION_EXECUTED_CONFIRMED"
)

func (s State) IsTerminal() bool {
	switch s {
	case StateDryRunComplete, StateHookConflict, StateHookUnavailable,
		StateNotDone, StateVerificationFailed, StateExpired, StateCancelled,
		StateOrphaned, StateHostUnavailable, StateInventoryPartial,
		StateConcurrentConflict, StatePlatformUnsupported,
		StatePrivilegeUnavailable, StateActionFailed,
		StateActionExecutionUnverified, StateActionExecutedConfirmed:
		return true
	default:
		return false
	}
}

func (s State) IsActive() bool {
	return !s.IsTerminal()
}

type Job struct {
	SchemaVersion          string                   `json:"schema_version"`
	JobID                  string                   `json:"job_id"`
	NonceHash              string                   `json:"nonce_hash"`
	State                  State                    `json:"state"`
	ReasonCode             string                   `json:"reason_code,omitempty"`
	DryRun                 bool                     `json:"dry_run"`
	Action                 string                   `json:"action"`
	TriggerPolicy          TriggerPolicy            `json:"trigger_policy"`
	StopWithoutSuccessAck  bool                     `json:"stop_without_success_acknowledged"`
	DelaySeconds           int64                    `json:"delay_seconds"`
	ExpiresAt              time.Time                `json:"expires_at"`
	CreatedAt              time.Time                `json:"created_at"`
	UpdatedAt              time.Time                `json:"updated_at"`
	SessionID              string                   `json:"session_id,omitempty"`
	ArmTurnID              string                   `json:"arm_turn_id,omitempty"`
	CurrentTurnID          string                   `json:"current_turn_id,omitempty"`
	ReadyTurnID            string                   `json:"ready_turn_id,omitempty"`
	StopTurnID             string                   `json:"stop_turn_id,omitempty"`
	Generation             uint64                   `json:"generation"`
	CompletionStatus       string                   `json:"completion_status,omitempty"`
	CompletionEvidenceHash string                   `json:"completion_evidence_hash,omitempty"`
	VerifierProfile        string                   `json:"verifier_profile"`
	AllowAgentOnlySuccess  bool                     `json:"allow_agent_only_success"`
	HookCompatibility      string                   `json:"hook_compatibility"`
	ArmObserved            bool                     `json:"arm_observed"`
	FinishObserved         bool                     `json:"finish_observed"`
	ProcessedEventKeys     []string                 `json:"processed_event_keys,omitempty"`
	WorkspaceCWD           string                   `json:"workspace_cwd,omitempty"`
	PowerPolicyFingerprint string                   `json:"power_policy_fingerprint,omitempty"`
	SupervisorPID          int                      `json:"supervisor_pid,omitempty"`
	VerifierFingerprint    string                   `json:"verifier_fingerprint,omitempty"`
	VerifierPassed         bool                     `json:"verifier_passed"`
	VerifierExitCode       *int                     `json:"verifier_exit_code,omitempty"`
	HookFingerprintH1      string                   `json:"hook_fingerprint_h1,omitempty"`
	HookFingerprintH2      string                   `json:"hook_fingerprint_h2,omitempty"`
	HookFingerprintH3      string                   `json:"hook_fingerprint_h3,omitempty"`
	HostSnapshotReason     string                   `json:"host_snapshot_reason,omitempty"`
	HostInstanceID         string                   `json:"host_instance_id,omitempty"`
	ActionIntentAt         *time.Time               `json:"action_intent_at,omitempty"`
	ScheduledFor           *time.Time               `json:"scheduled_for,omitempty"`
	PowerCapabilities      *actions.Capabilities    `json:"power_capabilities,omitempty"`
	PowerReceipt           *actions.Receipt         `json:"power_receipt,omitempty"`
	CancelResult           *actions.CancelResult    `json:"cancel_result,omitempty"`
	CancelRequested        bool                     `json:"cancel_requested"`
	CancelReason           string                   `json:"cancel_reason,omitempty"`
	ReconcileResult        *actions.ReconcileResult `json:"reconcile_result,omitempty"`
}

func (j Job) Expired(now time.Time) bool {
	return !j.ExpiresAt.IsZero() && !now.Before(j.ExpiresAt)
}

func HasUnresolvedPowerAction(job Job) bool {
	if job.DryRun {
		return false
	}
	if job.State == StateActionIntent || job.State == StateActionScheduled {
		return true
	}
	// Schema-2 supervisors briefly converted crash-recovery intent to ORPHANED.
	// Keep those local records cancellable and blocking during migration.
	return job.State == StateOrphaned && job.ActionIntentAt != nil && job.PowerCapabilities != nil
}

func RecoveryReceipt(job Job) (actions.Receipt, error) {
	if job.PowerReceipt != nil {
		if err := actions.ValidateReceipt(*job.PowerReceipt); err != nil {
			return actions.Receipt{}, err
		}
		return *job.PowerReceipt, nil
	}
	if job.PowerCapabilities == nil {
		return actions.Receipt{}, errors.New("power recovery capabilities are unavailable")
	}
	requestedAt := job.CreatedAt.UTC()
	if job.ActionIntentAt != nil {
		requestedAt = job.ActionIntentAt.UTC()
	}
	return actions.BuildIntentReceipt(job.JobID, job.Action, requestedAt, time.Duration(job.DelaySeconds)*time.Second, *job.PowerCapabilities)
}

func Validate(job Job) error {
	if job.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("unsupported plugin job schema %q", job.SchemaVersion)
	}
	if err := ValidateJobID(job.JobID); err != nil {
		return err
	}
	if !strings.HasPrefix(job.NonceHash, "sha256:") || len(job.NonceHash) != len("sha256:")+64 {
		return errors.New("invalid nonce hash")
	}
	switch job.State {
	case StateArmPendingBind, StateArmed, StateWaiting, StateVerifying,
		StateReadyPendingStop, StateStopObserved, StateDryRunComplete,
		StateHookConflict, StateHookUnavailable, StateNotDone,
		StateVerificationFailed, StateExpired, StateCancelled, StateOrphaned,
		StateHostMonitoring, StateActionIntent, StateActionScheduled,
		StateHostUnavailable, StateInventoryPartial, StateConcurrentConflict,
		StatePlatformUnsupported, StatePrivilegeUnavailable, StateActionFailed,
		StateActionExecutionUnverified, StateActionExecutedConfirmed:
	default:
		return fmt.Errorf("unknown plugin job state %q", job.State)
	}
	if job.Action != "shutdown" {
		return fmt.Errorf("unsupported plugin action %q", job.Action)
	}
	if !job.TriggerPolicy.Valid() {
		return fmt.Errorf("unsupported plugin trigger policy %q", job.TriggerPolicy)
	}
	if job.DelaySeconds < 30 || job.DelaySeconds > 3600 {
		return fmt.Errorf("invalid shutdown delay %d", job.DelaySeconds)
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() || job.ExpiresAt.IsZero() {
		return errors.New("plugin job timestamps are required")
	}
	if !job.ExpiresAt.After(job.CreatedAt) {
		return errors.New("plugin job expiry must be after creation")
	}
	if job.Generation == 0 {
		return errors.New("plugin job generation must be positive")
	}
	if job.VerifierProfile == "" {
		return errors.New("verifier profile is required")
	}
	if job.HookCompatibility == "" {
		return errors.New("hook compatibility is required")
	}
	if !job.DryRun {
		if !filepath.IsAbs(job.WorkspaceCWD) {
			return errors.New("execute plugin job requires an absolute workspace")
		}
		if job.TriggerPolicy == TriggerAfterStop {
			if !job.StopWithoutSuccessAck {
				return errors.New("after-stop execute job requires explicit stop-without-success acknowledgement")
			}
		} else {
			if job.VerifierProfile == "none" && !job.AllowAgentOnlySuccess {
				return errors.New("verified-success execute job requires a verifier or explicit agent-only approval")
			}
			if job.PowerPolicyFingerprint == "" {
				return errors.New("verified-success execute job requires a fixed power policy fingerprint")
			}
		}
	}
	if job.TriggerPolicy == TriggerAfterStop {
		if job.VerifierProfile != "none" || job.AllowAgentOnlySuccess {
			return errors.New("after-stop job cannot use semantic completion verification")
		}
		if job.PowerPolicyFingerprint != "" {
			return errors.New("after-stop job cannot carry a verified-success power policy")
		}
	} else if job.StopWithoutSuccessAck {
		return errors.New("verified-success job cannot acknowledge after-stop semantics")
	}
	if job.State == StateArmPendingBind {
		if job.SessionID != "" || job.ArmTurnID != "" || job.ArmObserved {
			return errors.New("pending plugin job cannot already be bound")
		}
	} else if job.State.IsActive() {
		if job.SessionID == "" || job.ArmTurnID == "" || !job.ArmObserved {
			return errors.New("active plugin job is missing its hook binding")
		}
	}
	if job.TriggerPolicy == TriggerVerifiedSuccess && (job.State == StateReadyPendingStop || job.State == StateStopObserved) &&
		(job.ReadyTurnID == "" || job.CompletionEvidenceHash == "") {
		return errors.New("ready plugin job is missing completion evidence")
	}
	if job.State == StateStopObserved && job.StopTurnID == "" {
		return errors.New("stop-observed plugin job is missing the stop turn")
	}
	if job.VerifierPassed && job.VerifierProfile != "none" && job.VerifierFingerprint == "" {
		return errors.New("verified plugin job is missing its verifier fingerprint")
	}
	if job.State == StateActionIntent {
		if job.ActionIntentAt == nil || job.PowerCapabilities == nil {
			return errors.New("action intent is missing its timestamp or capabilities")
		}
	}
	if job.PowerReceipt != nil {
		if err := actions.ValidateReceipt(*job.PowerReceipt); err != nil {
			return fmt.Errorf("invalid plugin power receipt: %w", err)
		}
		if job.PowerReceipt.JobID != job.JobID || job.PowerReceipt.Action != job.Action {
			return errors.New("plugin power receipt does not match the job")
		}
	}
	if job.CancelRequested && strings.TrimSpace(job.CancelReason) == "" {
		return errors.New("plugin cancellation request is missing its reason")
	}
	if job.State == StateActionScheduled || job.State == StateActionExecutionUnverified || job.State == StateActionExecutedConfirmed {
		if job.ActionIntentAt == nil || job.ScheduledFor == nil || job.PowerReceipt == nil {
			return errors.New("scheduled plugin job is missing its power receipt")
		}
	}
	if len(job.ProcessedEventKeys) > maxProcessedEventKeys {
		return fmt.Errorf("plugin job has more than %d event keys", maxProcessedEventKeys)
	}
	for _, key := range job.ProcessedEventKeys {
		if len(key) != 64 {
			return errors.New("invalid processed event key")
		}
	}
	return nil
}

func ValidateJobID(jobID string) error {
	if !strings.HasPrefix(jobID, "dt_") || len(jobID) < 6 || len(jobID) > 80 {
		return fmt.Errorf("invalid job id %q", jobID)
	}
	for _, char := range jobID {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return fmt.Errorf("invalid job id %q", jobID)
	}
	return nil
}
