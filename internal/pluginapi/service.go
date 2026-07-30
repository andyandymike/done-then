package pluginapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/completion"
	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

const (
	minimumExpirySeconds = int64(60)
	maximumExpirySeconds = int64(7 * 24 * 60 * 60)
)

type Result struct {
	Text       string
	Structured map[string]any
	IsError    bool
}

type Service struct {
	store *pluginstate.Store
	now   func() time.Time
}

func New(state *pluginstate.Store) (*Service, error) {
	if state == nil {
		return nil, errors.New("plugin state store is required")
	}
	return &Service{store: state, now: time.Now}, nil
}

func (s *Service) Call(_ context.Context, name string, raw json.RawMessage) Result {
	switch name {
	case "arm":
		return s.arm(raw)
	case "finish":
		return s.finish(raw)
	case "pause":
		return s.pause(raw)
	case "cancel":
		return s.cancel(raw)
	case "status":
		return s.status(raw)
	default:
		return failure(name, "unknown_tool", "DoneThen does not provide that tool")
	}
}

type armArguments struct {
	Action                string `json:"action"`
	DelaySeconds          int64  `json:"delay_seconds"`
	ExpiresInSeconds      int64  `json:"expires_in_seconds"`
	Mode                  string `json:"mode"`
	VerifierProfile       string `json:"verifier_profile"`
	AllowAgentOnlySuccess bool   `json:"allow_agent_only_success"`
}

func (s *Service) arm(raw json.RawMessage) Result {
	var args armArguments
	if err := decodeArguments(raw, &args); err != nil {
		return failure("arm", "invalid_arguments", err.Error())
	}
	if args.Action != "shutdown" {
		return failure("arm", "unsupported_action", "action must be shutdown")
	}
	if args.DelaySeconds < 30 || args.DelaySeconds > 3600 {
		return failure("arm", "invalid_delay", "delay_seconds must be between 30 and 3600")
	}
	if args.ExpiresInSeconds < minimumExpirySeconds || args.ExpiresInSeconds > maximumExpirySeconds {
		return failure("arm", "invalid_expiry", "expires_in_seconds must be between 60 and 604800")
	}
	if args.Mode == "execute" {
		return failure("arm", "execute_unavailable", "plugin execute mode is disabled until authoritative hook inventory is implemented")
	}
	if args.Mode != "dry_run" {
		return failure("arm", "invalid_mode", "mode must be dry_run or execute")
	}
	if args.VerifierProfile != "none" {
		return failure("arm", "verifier_profile_unavailable", "this build only supports verifier_profile=none in plugin dry-run mode")
	}
	jobIdentity, err := identity.New()
	if err != nil {
		return failure("arm", "state_error", "could not create a one-shot job identity")
	}
	now := s.now().UTC()
	job := pluginstate.Job{
		SchemaVersion:         "1",
		JobID:                 jobIdentity.JobID,
		NonceHash:             jobIdentity.NonceHash,
		State:                 pluginstate.StateArmPendingBind,
		ReasonCode:            "awaiting_post_tool_hook",
		DryRun:                true,
		Action:                args.Action,
		DelaySeconds:          args.DelaySeconds,
		ExpiresAt:             now.Add(time.Duration(args.ExpiresInSeconds) * time.Second),
		CreatedAt:             now,
		UpdatedAt:             now,
		Generation:            1,
		VerifierProfile:       args.VerifierProfile,
		AllowAgentOnlySuccess: args.AllowAgentOnlySuccess,
		HookCompatibility:     "not_evaluated",
	}
	if err := s.store.Create(job); err != nil {
		return failure("arm", "state_error", "could not persist the DoneThen job")
	}
	return success("arm", job, "Dry-run armed; waiting for the PostToolUse observer to bind this task")
}

type finishArguments struct {
	JobID      string          `json:"job_id"`
	Completion json.RawMessage `json:"completion"`
}

func (s *Service) finish(raw json.RawMessage) Result {
	var args finishArguments
	if err := decodeArguments(raw, &args); err != nil {
		return failure("finish", "invalid_arguments", err.Error())
	}
	if err := pluginstate.ValidateJobID(args.JobID); err != nil {
		return failure("finish", "invalid_job_id", "job_id is invalid")
	}
	envelope, err := completion.Parse(args.Completion)
	if err != nil {
		return failure("finish", "invalid_completion", err.Error())
	}
	decision := completion.Evaluate(envelope)
	evidenceHash := identity.SHA256(args.Completion)
	job, _, err := s.store.UpdateJob(args.JobID, "mcp.finish", "", func(job *pluginstate.Job, now time.Time) error {
		if expire(job, now) {
			return nil
		}
		if job.State != pluginstate.StateArmed {
			return fmt.Errorf("job is in state %s; finish requires ARMED", job.State)
		}
		job.Generation++
		job.CompletionStatus = string(envelope.Status)
		if !decision.Done {
			job.State = pluginstate.StateNotDone
			job.ReasonCode = "completion_policy_rejected"
			job.CompletionEvidenceHash = ""
			job.ReadyTurnID = ""
			return nil
		}
		if job.VerifierProfile == "none" && !job.AllowAgentOnlySuccess {
			job.State = pluginstate.StateVerificationFailed
			job.ReasonCode = "independent_evidence_required"
			job.CompletionEvidenceHash = ""
			job.ReadyTurnID = ""
			job.StopTurnID = ""
			job.FinishObserved = false
			return nil
		}
		job.State = pluginstate.StateReadyPendingStop
		job.ReasonCode = "completion_policy_passed"
		job.CompletionEvidenceHash = evidenceHash
		job.ReadyTurnID = job.CurrentTurnID
		job.StopTurnID = ""
		job.FinishObserved = false
		return nil
	})
	if err != nil {
		return failure("finish", "state_error", err.Error())
	}
	if job.State == pluginstate.StateExpired {
		return failureWithJob("finish", "job_expired", "the one-shot arm has expired", job)
	}
	if !decision.Done {
		return failureWithJob("finish", "not_done", decision.Reason, job)
	}
	if job.State == pluginstate.StateVerificationFailed {
		return failureWithJob("finish", "independent_evidence_required", "verifier_profile=none requires explicit allow_agent_only_success", job)
	}
	return success("finish", job, "Completion policy passed; waiting for the matching Stop observer; no action was scheduled")
}

type pauseArguments struct {
	JobID  string `json:"job_id"`
	Reason string `json:"reason"`
}

func (s *Service) pause(raw json.RawMessage) Result {
	var args pauseArguments
	if err := decodeArguments(raw, &args); err != nil {
		return failure("pause", "invalid_arguments", err.Error())
	}
	if !allowedPauseReason(args.Reason) {
		return failure("pause", "invalid_reason", "reason must be blocked, approval_required, waiting_for_user, or external_state")
	}
	job, _, err := s.store.UpdateJob(args.JobID, "mcp.pause", "", func(job *pluginstate.Job, now time.Time) error {
		if expire(job, now) {
			return nil
		}
		if job.State != pluginstate.StateArmed && job.State != pluginstate.StateReadyPendingStop && job.State != pluginstate.StateStopObserved {
			return fmt.Errorf("job is in state %s and cannot be paused", job.State)
		}
		job.Generation++
		job.State = pluginstate.StateWaiting
		job.ReasonCode = args.Reason
		clearCompletion(job)
		return nil
	})
	if err != nil {
		return failure("pause", "state_error", err.Error())
	}
	if job.State == pluginstate.StateExpired {
		return failureWithJob("pause", "job_expired", "the one-shot arm has expired", job)
	}
	return success("pause", job, "Job paused; no action can run while it is waiting")
}

type jobArguments struct {
	JobID string `json:"job_id"`
}

func (s *Service) cancel(raw json.RawMessage) Result {
	var args jobArguments
	if err := decodeArguments(raw, &args); err != nil {
		return failure("cancel", "invalid_arguments", err.Error())
	}
	job, _, err := s.store.UpdateJob(args.JobID, "mcp.cancel", "", func(job *pluginstate.Job, now time.Time) error {
		if job.State == pluginstate.StateCancelled {
			return nil
		}
		if job.State.IsTerminal() {
			return nil
		}
		job.Generation++
		job.State = pluginstate.StateCancelled
		job.ReasonCode = "cancelled_by_user"
		clearCompletion(job)
		return nil
	})
	if err != nil {
		return failure("cancel", "state_error", err.Error())
	}
	if job.State != pluginstate.StateCancelled {
		return success("cancel", job, "Job was already terminal; no power action is active")
	}
	return success("cancel", job, "Job cancelled; plugin mode never scheduled a power action")
}

func (s *Service) status(raw json.RawMessage) Result {
	var args jobArguments
	if len(bytes.TrimSpace(raw)) != 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := decodeArguments(raw, &args); err != nil {
			return failure("status", "invalid_arguments", err.Error())
		}
	}
	if args.JobID != "" {
		job, err := s.store.Load(args.JobID)
		if err != nil {
			return failure("status", "state_error", "could not load the DoneThen job")
		}
		if job.State.IsActive() && job.Expired(s.now().UTC()) {
			job, err = s.store.RefreshExpiry(job.JobID)
			if err != nil {
				return failure("status", "state_error", "could not refresh the DoneThen job")
			}
		}
		status := s.store.Status(job)
		return Result{
			Text: "DoneThen job status returned",
			Structured: map[string]any{
				"ok":                true,
				"tool":              "status",
				"job":               status,
				"execute_available": false,
			},
		}
	}
	jobs, err := s.store.List()
	if err != nil {
		return failure("status", "state_error", "could not list DoneThen jobs")
	}
	statuses := make([]pluginstate.Status, 0, len(jobs))
	for _, job := range jobs {
		if job.State.IsActive() && job.Expired(s.now().UTC()) {
			if refreshed, refreshErr := s.store.RefreshExpiry(job.JobID); refreshErr == nil {
				job = refreshed
			}
		}
		statuses = append(statuses, s.store.Status(job))
	}
	return Result{
		Text: "DoneThen plugin jobs returned",
		Structured: map[string]any{
			"ok":                true,
			"tool":              "status",
			"jobs":              statuses,
			"execute_available": false,
		},
	}
}

func success(tool string, job pluginstate.Job, message string) Result {
	return Result{
		Text: message,
		Structured: map[string]any{
			"ok":                  true,
			"tool":                tool,
			"job_id":              job.JobID,
			"state":               job.State,
			"reason_code":         job.ReasonCode,
			"generation":          job.Generation,
			"execute_available":   false,
			"power_action_called": false,
		},
	}
}

func failureWithJob(tool, code, message string, job pluginstate.Job) Result {
	result := failure(tool, code, message)
	result.Structured["job_id"] = job.JobID
	result.Structured["state"] = job.State
	result.Structured["generation"] = job.Generation
	return result
}

func failure(tool, code, message string) Result {
	return Result{
		Text:    message,
		IsError: true,
		Structured: map[string]any{
			"ok":                  false,
			"tool":                tool,
			"reason_code":         code,
			"execute_available":   false,
			"power_action_called": false,
		},
	}
}

func decodeArguments(raw json.RawMessage, destination any) error {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		raw = json.RawMessage("{}")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("arguments contain trailing JSON")
	}
	return nil
}

func allowedPauseReason(reason string) bool {
	switch reason {
	case "blocked", "approval_required", "waiting_for_user", "external_state":
		return true
	default:
		return false
	}
}

func expire(job *pluginstate.Job, now time.Time) bool {
	if job.State.IsActive() && job.Expired(now) {
		job.Generation++
		job.State = pluginstate.StateExpired
		job.ReasonCode = "arm_expired"
		clearCompletion(job)
		return true
	}
	return false
}

func clearCompletion(job *pluginstate.Job) {
	job.CompletionStatus = ""
	job.CompletionEvidenceHash = ""
	job.ReadyTurnID = ""
	job.StopTurnID = ""
	job.FinishObserved = false
}

func SanitizeError(err error) string {
	if err == nil {
		return ""
	}
	value := strings.TrimSpace(err.Error())
	if len(value) > 512 {
		value = value[:512]
	}
	return value
}
