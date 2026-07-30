package supervisor

import "time"

type State string

const (
	StateArmed                State = "ARMED"
	StateAgentRunning         State = "AGENT_RUNNING"
	StateEvaluating           State = "EVALUATING"
	StateVerifying            State = "VERIFYING"
	StateActionIntentRecorded State = "ACTION_INTENT_RECORDED"
	StateActionScheduled      State = "ACTION_SCHEDULED"
	StateDryRunComplete       State = "DRY_RUN_COMPLETE"
	StateNotDone              State = "NOT_DONE"
	StateAgentFailed          State = "AGENT_FAILED"
	StateInvalidCompletion    State = "INVALID_COMPLETION"
	StateVerificationFailed   State = "VERIFICATION_FAILED"
	StateActionFailed         State = "ACTION_FAILED"
	StateCancelled            State = "CANCELLED"
	StateOrphaned             State = "ORPHANED"
)

func (s State) IsTerminal() bool {
	switch s {
	case StateDryRunComplete, StateNotDone, StateAgentFailed, StateInvalidCompletion,
		StateVerificationFailed, StateActionFailed, StateCancelled, StateOrphaned:
		return true
	default:
		return false
	}
}

func (s State) IsUnresolvedPowerState() bool {
	return s == StateActionIntentRecorded || s == StateActionScheduled
}

func (s State) IsPreActionActive() bool {
	switch s {
	case StateArmed, StateAgentRunning, StateEvaluating, StateVerifying:
		return true
	default:
		return false
	}
}

type Job struct {
	SchemaVersion    string     `json:"schema_version"`
	JobID            string     `json:"job_id"`
	NonceHash        string     `json:"nonce_hash"`
	State            State      `json:"state"`
	ReasonCode       string     `json:"reason_code,omitempty"`
	DryRun           bool       `json:"dry_run"`
	Action           string     `json:"action"`
	DelaySeconds     int64      `json:"delay_seconds"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	SupervisorPID    int        `json:"supervisor_pid"`
	CodexPID         int        `json:"codex_pid"`
	CodexCWD         string     `json:"codex_cwd"`
	PromptSHA256     string     `json:"prompt_sha256"`
	CompletionStatus *string    `json:"completion_status"`
	VerifierExitCode *int       `json:"verifier_exit_code"`
	ActionExitCode   *int       `json:"action_exit_code"`
	ActionIntentAt   *time.Time `json:"action_intent_at"`
	ScheduledFor     *time.Time `json:"scheduled_for"`
	CancelRequested  bool       `json:"cancel_requested"`
}

type Event struct {
	Timestamp time.Time `json:"timestamp"`
	JobID     string    `json:"job_id"`
	OldState  State     `json:"old_state,omitempty"`
	NewState  State     `json:"new_state"`
	Reason    string    `json:"reason_code"`
	ExitCode  *int      `json:"exit_code,omitempty"`
	Duration  string    `json:"duration,omitempty"`
}
