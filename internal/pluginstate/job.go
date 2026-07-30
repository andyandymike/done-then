package pluginstate

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type State string

const (
	StateArmPendingBind     State = "ARM_PENDING_BIND"
	StateArmed              State = "ARMED"
	StateWaiting            State = "WAITING"
	StateVerifying          State = "VERIFYING"
	StateReadyPendingStop   State = "READY_PENDING_STOP"
	StateStopObserved       State = "STOP_OBSERVED"
	StateDryRunComplete     State = "DRY_RUN_COMPLETE"
	StateHookConflict       State = "HOOK_CONFLICT"
	StateHookUnavailable    State = "HOOK_UNAVAILABLE"
	StateNotDone            State = "NOT_DONE"
	StateVerificationFailed State = "VERIFICATION_FAILED"
	StateExpired            State = "EXPIRED"
	StateCancelled          State = "CANCELLED"
	StateOrphaned           State = "ORPHANED"
)

func (s State) IsTerminal() bool {
	switch s {
	case StateDryRunComplete, StateHookConflict, StateHookUnavailable,
		StateNotDone, StateVerificationFailed, StateExpired, StateCancelled,
		StateOrphaned:
		return true
	default:
		return false
	}
}

func (s State) IsActive() bool {
	return !s.IsTerminal()
}

type Job struct {
	SchemaVersion          string    `json:"schema_version"`
	JobID                  string    `json:"job_id"`
	NonceHash              string    `json:"nonce_hash"`
	State                  State     `json:"state"`
	ReasonCode             string    `json:"reason_code,omitempty"`
	DryRun                 bool      `json:"dry_run"`
	Action                 string    `json:"action"`
	DelaySeconds           int64     `json:"delay_seconds"`
	ExpiresAt              time.Time `json:"expires_at"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
	SessionID              string    `json:"session_id,omitempty"`
	ArmTurnID              string    `json:"arm_turn_id,omitempty"`
	CurrentTurnID          string    `json:"current_turn_id,omitempty"`
	ReadyTurnID            string    `json:"ready_turn_id,omitempty"`
	StopTurnID             string    `json:"stop_turn_id,omitempty"`
	Generation             uint64    `json:"generation"`
	CompletionStatus       string    `json:"completion_status,omitempty"`
	CompletionEvidenceHash string    `json:"completion_evidence_hash,omitempty"`
	VerifierProfile        string    `json:"verifier_profile"`
	AllowAgentOnlySuccess  bool      `json:"allow_agent_only_success"`
	HookCompatibility      string    `json:"hook_compatibility"`
	ArmObserved            bool      `json:"arm_observed"`
	FinishObserved         bool      `json:"finish_observed"`
	ProcessedEventKeys     []string  `json:"processed_event_keys,omitempty"`
}

func (j Job) Expired(now time.Time) bool {
	return !j.ExpiresAt.IsZero() && !now.Before(j.ExpiresAt)
}

func Validate(job Job) error {
	if job.SchemaVersion != "1" {
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
		StateVerificationFailed, StateExpired, StateCancelled, StateOrphaned:
	default:
		return fmt.Errorf("unknown plugin job state %q", job.State)
	}
	if !job.DryRun {
		return errors.New("plugin execute mode is unavailable before the hook-inventory gate")
	}
	if job.Action != "shutdown" {
		return fmt.Errorf("unsupported plugin action %q", job.Action)
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
	if job.State == StateArmPendingBind {
		if job.SessionID != "" || job.ArmTurnID != "" || job.ArmObserved {
			return errors.New("pending plugin job cannot already be bound")
		}
	} else if job.State.IsActive() {
		if job.SessionID == "" || job.ArmTurnID == "" || !job.ArmObserved {
			return errors.New("active plugin job is missing its hook binding")
		}
	}
	if (job.State == StateReadyPendingStop || job.State == StateStopObserved) &&
		(job.ReadyTurnID == "" || job.CompletionEvidenceHash == "") {
		return errors.New("ready plugin job is missing completion evidence")
	}
	if job.State == StateStopObserved && job.StopTurnID == "" {
		return errors.New("stop-observed plugin job is missing the stop turn")
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
