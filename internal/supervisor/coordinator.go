package supervisor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/completion"
	"github.com/andyandymike/done-then/internal/verifier"
)

const (
	ExitOK                  = 0
	ExitUsage               = 2
	ExitNotDone             = 10
	ExitAgentFailed         = 11
	ExitInvalidCompletion   = 12
	ExitVerificationFailed  = 13
	ExitActionFailed        = 14
	ExitCancelled           = 15
	ExitActiveJobConflict   = 16
	ExitStateError          = 17
	ExitInterrupted         = 130
	defaultCompletionReason = "completion_policy_passed"
)

type Store interface {
	Create(job Job) error
	Save(job Job) error
	Cancelled(jobID string) (bool, error)
	AppendEvent(event Event) error
}

type AgentResult struct {
	ExitCode      int
	PID           int
	Completion    []byte
	CompletionErr error
	Duration      time.Duration
}

type Agent interface {
	Run(ctx context.Context) (AgentResult, error)
}

type Verifier interface {
	Run(ctx context.Context) (verifier.Result, error)
}

type Config struct {
	JobID        string
	NonceHash    string
	DryRun       bool
	Action       string
	Delay        time.Duration
	CodexCWD     string
	PromptSHA256 string
	Agent        Agent
	Verifier     Verifier
	Backend      actions.Backend
	Store        Store
	Now          func() time.Time
}

type Outcome struct {
	JobID                 string
	State                 State
	Reason                string
	ExitCode              int
	ActionMayBeScheduled  bool
	CompletionSummaryOnly string
}

type Coordinator struct {
	config Config
}

func New(config Config) (*Coordinator, error) {
	if config.JobID == "" || config.NonceHash == "" {
		return nil, errors.New("job identity is required")
	}
	if config.Action != "shutdown" {
		return nil, fmt.Errorf("unsupported action %q", config.Action)
	}
	if config.Delay < 30*time.Second || config.Delay > time.Hour {
		return nil, errors.New("shutdown delay must be between 30s and 1h")
	}
	if config.Delay%time.Second != 0 {
		return nil, errors.New("shutdown delay must use whole seconds")
	}
	if config.Agent == nil || config.Store == nil {
		return nil, errors.New("agent and store are required")
	}
	if !config.DryRun && config.Backend == nil {
		return nil, errors.New("action backend is required in execute mode")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Coordinator{config: config}, nil
}

func (c *Coordinator) Run(ctx context.Context) Outcome {
	now := c.config.Now().UTC()
	job := Job{
		SchemaVersion: "2",
		JobID:         c.config.JobID,
		NonceHash:     c.config.NonceHash,
		State:         StateArmed,
		ReasonCode:    "job_armed",
		DryRun:        c.config.DryRun,
		Action:        c.config.Action,
		DelaySeconds:  int64(c.config.Delay / time.Second),
		CreatedAt:     now,
		UpdatedAt:     now,
		SupervisorPID: os.Getpid(),
		CodexCWD:      c.config.CodexCWD,
		PromptSHA256:  c.config.PromptSHA256,
	}
	if err := c.config.Store.Create(job); err != nil {
		return storageOutcome(job, "create job record", err, false)
	}
	c.logTransition("", job, "job_armed", nil, 0)

	if cancelled, outcome := c.cancelled(job, "cancelled_before_agent"); cancelled {
		return outcome
	}
	if outcome := c.move(&job, StateAgentRunning, "agent_started", nil, 0); outcome != nil {
		return *outcome
	}

	result, agentErr := c.config.Agent.Run(ctx)
	job.CodexPID = result.PID
	if ctx.Err() != nil {
		if outcome := c.move(&job, StateCancelled, "foreground_interrupted", nil, result.Duration); outcome != nil {
			return *outcome
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: "foreground process was interrupted", ExitCode: ExitInterrupted}
	}
	if agentErr != nil || result.ExitCode != 0 {
		exit := result.ExitCode
		if outcome := c.move(&job, StateAgentFailed, "agent_failed", &exit, result.Duration); outcome != nil {
			return *outcome
		}
		reason := "Codex failed"
		if agentErr != nil {
			reason = agentErr.Error()
		} else {
			reason = fmt.Sprintf("Codex exited with code %d", result.ExitCode)
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: reason, ExitCode: ExitAgentFailed}
	}
	if cancelled, outcome := c.cancelled(job, "cancelled_after_agent"); cancelled {
		return outcome
	}
	if outcome := c.move(&job, StateEvaluating, "agent_completed", nil, result.Duration); outcome != nil {
		return *outcome
	}
	if result.CompletionErr != nil {
		if outcome := c.move(&job, StateInvalidCompletion, "invalid_completion", nil, 0); outcome != nil {
			return *outcome
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: result.CompletionErr.Error(), ExitCode: ExitInvalidCompletion}
	}

	envelope, err := completion.Parse(result.Completion)
	if err != nil {
		if outcome := c.move(&job, StateInvalidCompletion, "invalid_completion", nil, 0); outcome != nil {
			return *outcome
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: err.Error(), ExitCode: ExitInvalidCompletion}
	}
	status := string(envelope.Status)
	job.CompletionStatus = &status
	decision := completion.Evaluate(envelope)
	if !decision.Done {
		if outcome := c.move(&job, StateNotDone, "completion_not_done", nil, 0); outcome != nil {
			return *outcome
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: decision.Reason, ExitCode: ExitNotDone, CompletionSummaryOnly: envelope.Summary}
	}

	if c.config.Verifier != nil {
		if outcome := c.move(&job, StateVerifying, "verifier_started", nil, 0); outcome != nil {
			return *outcome
		}
		if cancelled, outcome := c.cancelled(job, "cancelled_before_verifier"); cancelled {
			return outcome
		}
		verifyResult, verifyErr := c.config.Verifier.Run(ctx)
		job.VerifierExitCode = &verifyResult.ExitCode
		if ctx.Err() != nil {
			if outcome := c.move(&job, StateCancelled, "foreground_interrupted", nil, verifyResult.Duration); outcome != nil {
				return *outcome
			}
			return Outcome{JobID: job.JobID, State: job.State, Reason: "foreground process was interrupted", ExitCode: ExitInterrupted}
		}
		if verifyErr != nil || verifyResult.ExitCode != 0 {
			exit := verifyResult.ExitCode
			if outcome := c.move(&job, StateVerificationFailed, "verifier_failed", &exit, verifyResult.Duration); outcome != nil {
				return *outcome
			}
			reason := fmt.Sprintf("verifier exited with code %d", verifyResult.ExitCode)
			if verifyErr != nil {
				reason = verifyErr.Error()
			}
			return Outcome{JobID: job.JobID, State: job.State, Reason: reason, ExitCode: ExitVerificationFailed}
		}
		if cancelled, outcome := c.cancelled(job, "cancelled_after_verifier"); cancelled {
			return outcome
		}
	}

	if c.config.DryRun {
		if outcome := c.move(&job, StateDryRunComplete, "dry_run_complete", nil, 0); outcome != nil {
			return *outcome
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: "all completion gates passed; dry-run performed no action", ExitCode: ExitOK, CompletionSummaryOnly: envelope.Summary}
	}
	if cancelled, outcome := c.cancelled(job, "cancelled_before_action"); cancelled {
		return outcome
	}

	comment := actions.ClassicPowerComment(job.JobID)
	request := actions.PowerRequest{
		JobID:       job.JobID,
		Action:      job.Action,
		Delay:       c.config.Delay,
		Comment:     comment,
		RequestedAt: c.config.Now().UTC(),
	}
	capabilities, err := c.config.Backend.Preflight(ctx, request)
	if err != nil || !capabilities.ExecuteSupported {
		exit := 1
		job.PowerCapabilities = &capabilities
		if outcome := c.move(&job, StateActionFailed, "platform_preflight_failed", &exit, 0); outcome != nil {
			return *outcome
		}
		reason := "platform action preflight rejected shutdown"
		if err != nil {
			reason = err.Error()
		} else if capabilities.Reason != "" {
			reason = capabilities.Reason
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: reason, ExitCode: ExitActionFailed}
	}
	job.PowerCapabilities = &capabilities

	intentAt := request.RequestedAt.UTC()
	job.ActionIntentAt = &intentAt
	intentReceipt, intentErr := actions.BuildIntentReceipt(job.JobID, job.Action, intentAt, request.Delay, capabilities)
	if intentErr != nil {
		exit := 1
		if outcome := c.move(&job, StateActionFailed, "intent_recovery_receipt_failed", &exit, 0); outcome != nil {
			return *outcome
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: intentErr.Error(), ExitCode: ExitStateError}
	}
	job.PowerReceipt = &intentReceipt
	intentDeadline := intentReceipt.Deadline.UTC()
	job.ScheduledFor = &intentDeadline
	if outcome := c.move(&job, StateActionIntentRecorded, defaultCompletionReason, nil, 0); outcome != nil {
		return *outcome
	}
	if cancelled, outcome := c.cancelled(job, "cancelled_at_action_boundary"); cancelled {
		return outcome
	}

	receipt, err := c.config.Backend.Schedule(ctx, request)
	if err != nil {
		job.ReasonCode = "power_schedule_outcome_unknown"
		if saveErr := c.config.Store.Save(job); saveErr != nil {
			return storageOutcome(job, "persist unresolved schedule outcome", saveErr, true)
		}
		cancelRequested, cancelReadErr := c.config.Store.Cancelled(job.JobID)
		if cancelRequested || cancelReadErr != nil {
			cancelResult, cancelErr := c.config.Backend.Cancel(ctx, intentReceipt)
			job.CancelResult = &cancelResult
			if cancelErr == nil || errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
				job.CancelRequested = true
				reason := "cancelled_after_unknown_schedule_outcome"
				exitCode := ExitCancelled
				message := "the schedule result was unknown, but the requested cancellation was confirmed"
				if cancelReadErr != nil {
					reason = "cancel_state_unreadable_unknown_action_rolled_back"
					exitCode = ExitStateError
					message = "cancellation state was unreadable, so the unknown action was conservatively rolled back"
				}
				if outcome := c.move(&job, StateCancelled, reason, nil, 0); outcome != nil {
					outcome.ActionMayBeScheduled = false
					return *outcome
				}
				return Outcome{JobID: job.JobID, State: job.State, Reason: message, ExitCode: exitCode}
			}
			job.ReasonCode = "unknown_schedule_cancel_unconfirmed"
			if saveErr := c.config.Store.Save(job); saveErr != nil {
				return storageOutcome(job, "persist unconfirmed cancellation after unknown schedule outcome", saveErr, true)
			}
			return Outcome{
				JobID: job.JobID, State: job.State,
				Reason:               fmt.Sprintf("schedule outcome is unknown and cancellation was not confirmed: schedule: %v; cancel: %v", err, cancelErr),
				ExitCode:             ExitActionFailed,
				ActionMayBeScheduled: true,
			}
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: err.Error() + "; cancel or reconcile the unresolved action intent", ExitCode: ExitActionFailed, ActionMayBeScheduled: true}
	}
	if err := actions.ValidateReceiptForRequest(receipt, request, capabilities); err != nil {
		reason := "backend returned a receipt that does not match the power request"
		reason = fmt.Sprintf("backend returned an invalid power receipt: %v", err)
		cancelResult, cancelErr := c.config.Backend.Cancel(ctx, intentReceipt)
		job.CancelResult = &cancelResult
		if cancelErr == nil || errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
			exit := 1
			if outcome := c.move(&job, StateActionFailed, "invalid_receipt_action_rolled_back", &exit, 0); outcome != nil {
				return *outcome
			}
			return Outcome{JobID: job.JobID, State: job.State, Reason: reason + "; recovery cancellation completed", ExitCode: ExitStateError}
		}
		job.ReasonCode = "invalid_receipt_cancel_unconfirmed"
		if saveErr := c.config.Store.Save(job); saveErr != nil {
			return storageOutcome(job, "persist unconfirmed recovery cancellation", saveErr, true)
		}
		return Outcome{
			JobID:                job.JobID,
			State:                job.State,
			Reason:               reason + "; recovery cancellation was not confirmed",
			ExitCode:             ExitStateError,
			ActionMayBeScheduled: true,
		}
	}

	exit := 0
	job.ActionExitCode = &exit
	job.PowerReceipt = &receipt
	scheduledFor := receipt.Deadline.UTC()
	job.ScheduledFor = &scheduledFor
	if outcome := c.move(&job, StateActionScheduled, "action_scheduled", &exit, 0); outcome != nil {
		cancelResult, cancelErr := c.config.Backend.Cancel(ctx, receipt)
		job.CancelResult = &cancelResult
		if cancelErr == nil || errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
			if rollbackOutcome := c.move(&job, StateCancelled, "scheduled_state_persist_failed_rolled_back", nil, 0); rollbackOutcome != nil {
				rollbackOutcome.ActionMayBeScheduled = true
				rollbackOutcome.Reason = "the platform accepted shutdown and rollback ran, but the cancelled state could not be persisted; run donethen cancel " + job.JobID
				return *rollbackOutcome
			}
			return Outcome{JobID: job.JobID, State: job.State, Reason: "the scheduled state could not be persisted; the platform action was rolled back", ExitCode: ExitStateError}
		}
		outcome.ActionMayBeScheduled = true
		outcome.Reason = "the platform accepted shutdown, but neither scheduled-state persistence nor rollback was confirmed; run donethen cancel " + job.JobID
		return *outcome
	}
	cancelledAfterSchedule, cancelErr := c.config.Store.Cancelled(job.JobID)
	if cancelErr != nil {
		cancelResult, rollbackErr := c.config.Backend.Cancel(ctx, receipt)
		job.CancelResult = &cancelResult
		if rollbackErr == nil || errors.Is(rollbackErr, actions.ErrNoShutdownInProgress) {
			job.CancelRequested = true
			if outcome := c.move(&job, StateCancelled, "cancel_state_unreadable_action_rolled_back", nil, 0); outcome != nil {
				outcome.ActionMayBeScheduled = false
				outcome.Reason = fmt.Sprintf("cancellation state could not be rechecked, but backend rollback completed; persist cancelled state: %v", outcome.Reason)
				return *outcome
			}
			return Outcome{
				JobID: job.JobID, State: job.State,
				Reason:   fmt.Sprintf("cancellation state could not be rechecked, so the accepted shutdown was rolled back: %v", cancelErr),
				ExitCode: ExitStateError,
			}
		}
		return Outcome{
			JobID:                job.JobID,
			State:                job.State,
			Reason:               fmt.Sprintf("the platform accepted shutdown, but cancellation state could not be rechecked and rollback was not confirmed: %v; rollback: %v", cancelErr, rollbackErr),
			ExitCode:             ExitStateError,
			ActionMayBeScheduled: true,
		}
	}
	if cancelledAfterSchedule {
		cancelResult, cancelErr := c.config.Backend.Cancel(ctx, receipt)
		job.CancelResult = &cancelResult
		if cancelErr != nil && !errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
			return Outcome{
				JobID:                job.JobID,
				State:                job.State,
				Reason:               fmt.Sprintf("cancellation was requested after scheduling, but backend cancellation failed: %v", cancelErr),
				ExitCode:             ExitActionFailed,
				ActionMayBeScheduled: true,
			}
		}
		job.CancelRequested = true
		if outcome := c.move(&job, StateCancelled, "cancelled_after_schedule", nil, 0); outcome != nil {
			outcome.ActionMayBeScheduled = true
			return *outcome
		}
		return Outcome{JobID: job.JobID, State: job.State, Reason: "shutdown countdown was cancelled", ExitCode: ExitCancelled}
	}
	return Outcome{JobID: job.JobID, State: job.State, Reason: fmt.Sprintf("shutdown scheduled by %s for %s", receipt.BackendID, scheduledFor.Format(time.RFC3339)), ExitCode: ExitOK, ActionMayBeScheduled: true, CompletionSummaryOnly: envelope.Summary}
}

func (c *Coordinator) cancelled(job Job, reason string) (bool, Outcome) {
	cancelled, err := c.config.Store.Cancelled(job.JobID)
	if err != nil {
		return true, storageOutcome(job, "read cancellation state", err, false)
	}
	if !cancelled {
		return false, Outcome{}
	}
	job.CancelRequested = true
	if outcome := c.move(&job, StateCancelled, reason, nil, 0); outcome != nil {
		return true, *outcome
	}
	return true, Outcome{JobID: job.JobID, State: job.State, Reason: "action was cancelled by the user", ExitCode: ExitCancelled}
}

func (c *Coordinator) move(job *Job, next State, reason string, exitCode *int, duration time.Duration) *Outcome {
	old := job.State
	if err := Transition(old, next); err != nil {
		outcome := storageOutcome(*job, "apply state transition", err, next == StateActionScheduled)
		return &outcome
	}
	job.State = next
	job.ReasonCode = reason
	job.UpdatedAt = c.config.Now().UTC()
	if err := c.config.Store.Save(*job); err != nil {
		outcome := storageOutcome(*job, "persist state transition", err, next == StateActionScheduled)
		return &outcome
	}
	c.logTransition(old, *job, reason, exitCode, duration)
	return nil
}

func (c *Coordinator) logTransition(old State, job Job, reason string, exitCode *int, duration time.Duration) {
	event := Event{
		Timestamp: c.config.Now().UTC(),
		JobID:     job.JobID,
		OldState:  old,
		NewState:  job.State,
		Reason:    reason,
		ExitCode:  exitCode,
	}
	if duration > 0 {
		event.Duration = duration.Round(time.Millisecond).String()
	}
	_ = c.config.Store.AppendEvent(event)
}

func storageOutcome(job Job, operation string, err error, actionMayBeScheduled bool) Outcome {
	return Outcome{
		JobID:                job.JobID,
		State:                job.State,
		Reason:               fmt.Sprintf("%s: %v", operation, err),
		ExitCode:             ExitStateError,
		ActionMayBeScheduled: actionMayBeScheduled,
	}
}
