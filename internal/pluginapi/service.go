package pluginapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/completion"
	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/verifierprofile"
)

const (
	minimumExpirySeconds = int64(60)
	maximumExpirySeconds = int64(24 * 60 * 60)
)

type Result struct {
	Text       string
	Structured map[string]any
	IsError    bool
}

type Service struct {
	store                             *pluginstate.Store
	now                               func() time.Time
	afterStopExecuteAvailable         bool
	afterStopExecuteUnavailableReason string
	verifiedExecuteAvailable          bool
	verifiedExecuteUnavailableReason  string
	workspace                         string
	profiles                          *verifierprofile.Registry
	launcher                          JobLauncher
	backend                           actions.Backend
	powerPolicyFingerprint            string
	allowAgentOnlySuccess             bool
}

type JobLauncher interface {
	Launch(jobID string) (int, error)
}

type Options struct {
	AfterStopExecuteAvailable         bool
	AfterStopExecuteUnavailableReason string
	ExecuteAvailable                  bool
	ExecuteUnavailableReason          string
	Workspace                         string
	Profiles                          *verifierprofile.Registry
	Launcher                          JobLauncher
	Backend                           actions.Backend
	PowerPolicyFingerprint            string
	AllowAgentOnlySuccess             bool
}

func New(state *pluginstate.Store) (*Service, error) {
	return NewWithOptions(state, Options{})
}

func NewWithOptions(state *pluginstate.Store, options Options) (*Service, error) {
	if state == nil {
		return nil, errors.New("plugin state store is required")
	}
	if options.AfterStopExecuteAvailable || options.ExecuteAvailable {
		if !filepath.IsAbs(options.Workspace) {
			return nil, errors.New("plugin execute requires an absolute workspace")
		}
		if options.Launcher == nil || options.Backend == nil {
			return nil, errors.New("plugin execute requires a launcher and power backend")
		}
	}
	if options.ExecuteAvailable {
		if options.Profiles == nil || options.PowerPolicyFingerprint == "" {
			return nil, errors.New("verified-success execute requires profiles and a fixed power policy")
		}
	}
	return &Service{
		store:                             state,
		now:                               time.Now,
		afterStopExecuteAvailable:         options.AfterStopExecuteAvailable,
		afterStopExecuteUnavailableReason: options.AfterStopExecuteUnavailableReason,
		verifiedExecuteAvailable:          options.ExecuteAvailable,
		verifiedExecuteUnavailableReason:  options.ExecuteUnavailableReason,
		workspace:                         options.Workspace,
		profiles:                          options.Profiles,
		launcher:                          options.Launcher,
		backend:                           options.Backend,
		powerPolicyFingerprint:            options.PowerPolicyFingerprint,
		allowAgentOnlySuccess:             options.AllowAgentOnlySuccess,
	}, nil
}

func (s *Service) Call(ctx context.Context, name string, raw json.RawMessage) Result {
	var result Result
	switch name {
	case "arm":
		result = s.arm(ctx, raw)
	case "finish":
		result = s.finish(ctx, raw)
	case "pause":
		result = s.pause(raw)
	case "cancel":
		result = s.cancel(ctx, raw)
	case "status":
		result = s.status(raw)
	default:
		result = failure(name, "unknown_tool", "DoneThen does not provide that tool")
	}
	result.Structured["execute_available"] = s.afterStopExecuteAvailable || s.verifiedExecuteAvailable
	result.Structured["after_stop_execute_available"] = s.afterStopExecuteAvailable
	result.Structured["verified_success_execute_available"] = s.verifiedExecuteAvailable
	return result
}

type armArguments struct {
	Action                        string `json:"action"`
	TriggerPolicy                 string `json:"trigger_policy"`
	AcknowledgeStopWithoutSuccess bool   `json:"acknowledge_stop_without_success"`
	DelaySeconds                  int64  `json:"delay_seconds"`
	ExpiresInSeconds              int64  `json:"expires_in_seconds"`
	Mode                          string `json:"mode"`
	VerifierProfile               string `json:"verifier_profile"`
	AllowAgentOnlySuccess         bool   `json:"allow_agent_only_success"`
}

func (s *Service) arm(ctx context.Context, raw json.RawMessage) Result {
	var args armArguments
	if err := decodeArguments(raw, &args); err != nil {
		return failure("arm", "invalid_arguments", err.Error())
	}
	triggerPolicy := pluginstate.TriggerPolicy(args.TriggerPolicy)
	if triggerPolicy == "" {
		triggerPolicy = pluginstate.TriggerAfterStop
	}
	if !triggerPolicy.Valid() {
		return failure("arm", "invalid_trigger_policy", "trigger_policy must be after_stop or verified_success")
	}
	if args.Action != "shutdown" {
		return failure("arm", "unsupported_action", "action must be shutdown")
	}
	if args.DelaySeconds < 30 || args.DelaySeconds > 3600 {
		return failure("arm", "invalid_delay", "delay_seconds must be between 30 and 3600")
	}
	if args.Mode == "execute" && args.DelaySeconds < 120 {
		return failure("arm", "invalid_delay", "execute mode requires delay_seconds between 120 and 3600")
	}
	if args.ExpiresInSeconds < minimumExpirySeconds || args.ExpiresInSeconds > maximumExpirySeconds {
		return failure("arm", "invalid_expiry", "expires_in_seconds must be between 60 and 86400")
	}
	if triggerPolicy == pluginstate.TriggerAfterStop {
		if args.VerifierProfile == "" {
			args.VerifierProfile = "none"
		}
		if args.VerifierProfile != "none" || args.AllowAgentOnlySuccess {
			return failure("arm", "semantic_verification_not_applicable", "after_stop must use verifier_profile=none and allow_agent_only_success=false")
		}
	} else if args.AcknowledgeStopWithoutSuccess {
		return failure("arm", "invalid_acknowledgement", "verified_success cannot acknowledge after-stop semantics")
	}
	if args.Mode != "dry_run" {
		if args.Mode != "execute" {
			return failure("arm", "invalid_mode", "mode must be dry_run or execute")
		}
		available := s.afterStopExecuteAvailable
		reason := s.afterStopExecuteUnavailableReason
		if triggerPolicy == pluginstate.TriggerVerifiedSuccess {
			available = s.verifiedExecuteAvailable
			reason = s.verifiedExecuteUnavailableReason
		}
		if !available {
			if reason == "" {
				if triggerPolicy == pluginstate.TriggerAfterStop {
					reason = "after-stop execute is unavailable on this platform"
				} else {
					reason = "verified-success execute requires validated local policy and same-host authority"
				}
			}
			return failure("arm", "execute_unavailable", reason)
		}
		if triggerPolicy == pluginstate.TriggerAfterStop && !args.AcknowledgeStopWithoutSuccess {
			return failure("arm", "stop_without_success_acknowledgement_required", "after_stop execute requires explicit acknowledgement that any normal Stop for the armed turn can trigger shutdown regardless of task success")
		}
	}
	if triggerPolicy == pluginstate.TriggerVerifiedSuccess && args.Mode == "dry_run" && args.VerifierProfile != "none" {
		return failure("arm", "verifier_profile_unavailable", "this build only supports verifier_profile=none in plugin dry-run mode")
	}
	var verifierFingerprint string
	if triggerPolicy == pluginstate.TriggerVerifiedSuccess && args.Mode == "execute" {
		if args.VerifierProfile == "none" {
			if !args.AllowAgentOnlySuccess || !s.allowAgentOnlySuccess {
				return failure("arm", "independent_evidence_required", "execute requires a registered verifier profile or explicit allow_agent_only_success")
			}
		} else {
			profile, err := s.profiles.Load(args.VerifierProfile)
			if err != nil {
				return failure("arm", "verifier_profile_unavailable", "the registered verifier profile is unavailable or invalid")
			}
			verifierFingerprint = profile.Fingerprint
		}
	}
	jobIdentity, err := identity.New()
	if err != nil {
		return failure("arm", "state_error", "could not create a one-shot job identity")
	}
	now := s.now().UTC()
	if triggerPolicy == pluginstate.TriggerAfterStop && args.Mode == "execute" {
		request := actions.PowerRequest{
			JobID: jobIdentity.JobID, Action: args.Action, Delay: time.Duration(args.DelaySeconds) * time.Second,
			Comment: "DoneThen after-stop preflight", RequestedAt: now,
		}
		capabilities, preflightErr := s.backend.Preflight(ctx, request)
		if preflightErr != nil || !capabilities.ExecuteSupported {
			return failure("arm", "power_unavailable", "the local power backend did not pass after-stop preflight")
		}
	}
	powerPolicyFingerprint := ""
	if triggerPolicy == pluginstate.TriggerVerifiedSuccess {
		powerPolicyFingerprint = s.powerPolicyFingerprint
	}
	job := pluginstate.Job{
		SchemaVersion:          pluginstate.CurrentSchemaVersion,
		JobID:                  jobIdentity.JobID,
		NonceHash:              jobIdentity.NonceHash,
		State:                  pluginstate.StateArmPendingBind,
		ReasonCode:             "awaiting_post_tool_hook",
		DryRun:                 args.Mode == "dry_run",
		Action:                 args.Action,
		TriggerPolicy:          triggerPolicy,
		StopWithoutSuccessAck:  args.AcknowledgeStopWithoutSuccess,
		DelaySeconds:           args.DelaySeconds,
		ExpiresAt:              now.Add(time.Duration(args.ExpiresInSeconds) * time.Second),
		CreatedAt:              now,
		UpdatedAt:              now,
		Generation:             1,
		VerifierProfile:        args.VerifierProfile,
		AllowAgentOnlySuccess:  args.AllowAgentOnlySuccess,
		HookCompatibility:      "not_evaluated",
		WorkspaceCWD:           s.workspace,
		VerifierFingerprint:    verifierFingerprint,
		PowerPolicyFingerprint: powerPolicyFingerprint,
	}
	if err := s.store.Create(job); err != nil {
		return failure("arm", "state_error", "could not persist the DoneThen job")
	}
	if !job.DryRun {
		pid, launchErr := s.launcher.Launch(job.JobID)
		if launchErr != nil {
			failed, _, _ := s.store.UpdateJob(job.JobID, "supervisor.launch_failed", "", func(job *pluginstate.Job, _ time.Time) error {
				job.Generation++
				job.State = pluginstate.StateOrphaned
				job.ReasonCode = "supervisor_launch_failed"
				return nil
			})
			return failureWithJob("arm", "supervisor_unavailable", "the one-shot supervisor could not be started", failed)
		}
		job, _, err = s.store.UpdateJob(job.JobID, "supervisor.launched", "", func(job *pluginstate.Job, _ time.Time) error {
			job.SupervisorPID = pid
			return nil
		})
		if err != nil {
			return failureWithJob("arm", "state_error", "the supervisor started but its identity could not be persisted; cancel this job", job)
		}
		if triggerPolicy == pluginstate.TriggerAfterStop {
			return success("arm", job, "After-stop shutdown armed; the normal matching Stop can start the cancellable countdown")
		}
		return success("arm", job, "Verified-success job armed; the supervisor is waiting for hook binding and host evidence")
	}
	if triggerPolicy == pluginstate.TriggerAfterStop {
		return success("arm", job, "After-stop dry-run armed; the normal matching Stop will be recorded without scheduling power")
	}
	return success("arm", job, "Verified-success dry-run armed; waiting for completion evidence")
}

type finishArguments struct {
	JobID      string          `json:"job_id"`
	Completion json.RawMessage `json:"completion"`
}

func (s *Service) finish(ctx context.Context, raw json.RawMessage) Result {
	var args finishArguments
	if err := decodeArguments(raw, &args); err != nil {
		return failure("finish", "invalid_arguments", err.Error())
	}
	if err := pluginstate.ValidateJobID(args.JobID); err != nil {
		return failure("finish", "invalid_job_id", "job_id is invalid")
	}
	current, err := s.store.Load(args.JobID)
	if err != nil {
		return failure("finish", "state_error", "could not load the DoneThen job")
	}
	if current.TriggerPolicy == pluginstate.TriggerAfterStop {
		return failureWithJob("finish", "finish_not_required", "after_stop is driven only by the matching Stop hook; semantic completion is not evaluated", current)
	}
	envelope, err := completion.Parse(args.Completion)
	if err != nil {
		return failure("finish", "invalid_completion", err.Error())
	}
	decision := completion.Evaluate(envelope)
	evidenceHash := identity.SHA256(args.Completion)
	shouldVerify := false
	job, _, err := s.store.UpdateJob(args.JobID, "mcp.finish", "", func(job *pluginstate.Job, now time.Time) error {
		if expire(job, now) {
			return nil
		}
		if job.State != pluginstate.StateArmed && job.State != pluginstate.StateHostMonitoring {
			return fmt.Errorf("job is in state %s; finish requires ARMED or HOST_MONITORING", job.State)
		}
		job.Generation++
		job.CompletionStatus = string(envelope.Status)
		job.VerifierPassed = false
		job.VerifierExitCode = nil
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
		job.CompletionEvidenceHash = evidenceHash
		job.ReadyTurnID = job.CurrentTurnID
		job.StopTurnID = ""
		job.FinishObserved = false
		job.HookFingerprintH2 = ""
		job.HookFingerprintH3 = ""
		if job.VerifierProfile == "none" {
			job.State = pluginstate.StateReadyPendingStop
			job.ReasonCode = "completion_policy_passed_agent_only"
			return nil
		}
		job.State = pluginstate.StateVerifying
		job.ReasonCode = "verifier_started"
		shouldVerify = true
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
	if shouldVerify {
		expectedGeneration := job.Generation
		verifyExit := -1
		verifyErr := errors.New("registered verifier profile is unavailable")
		profile, loadErr := s.profiles.Load(job.VerifierProfile)
		if loadErr == nil && profile.Fingerprint != job.VerifierFingerprint {
			loadErr = errors.New("registered verifier profile changed after arm")
		}
		if loadErr == nil {
			runner, runnerErr := profile.Runner(job.WorkspaceCWD, io.Discard, io.Discard)
			if runnerErr == nil {
				result, runErr := runner.Run(ctx)
				verifyExit = result.ExitCode
				verifyErr = runErr
			} else {
				verifyErr = runnerErr
			}
		} else {
			verifyErr = loadErr
		}
		job, _, err = s.store.UpdateJob(args.JobID, "mcp.finish.verifier", "", func(job *pluginstate.Job, now time.Time) error {
			if expire(job, now) || job.State == pluginstate.StateCancelled {
				return nil
			}
			if job.State != pluginstate.StateVerifying || job.Generation != expectedGeneration {
				return errors.New("job changed while the verifier was running")
			}
			job.VerifierExitCode = &verifyExit
			if verifyErr != nil || verifyExit != 0 {
				job.State = pluginstate.StateVerificationFailed
				job.ReasonCode = "verifier_failed"
				job.VerifierPassed = false
				job.CompletionEvidenceHash = ""
				job.ReadyTurnID = ""
				return nil
			}
			job.VerifierPassed = true
			job.State = pluginstate.StateReadyPendingStop
			job.ReasonCode = "completion_and_verifier_passed"
			return nil
		})
		if err != nil {
			return failure("finish", "state_error", err.Error())
		}
		if job.State == pluginstate.StateVerificationFailed {
			return failureWithJob("finish", "verification_failed", "the registered verifier did not pass; no action can run", job)
		}
		if job.State == pluginstate.StateCancelled || job.State == pluginstate.StateExpired {
			return failureWithJob("finish", "job_inactive", "the job became inactive while verification was running", job)
		}
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
	current, err := s.store.Load(args.JobID)
	if err != nil {
		return failure("pause", "state_error", "could not load the DoneThen job")
	}
	if current.TriggerPolicy == pluginstate.TriggerAfterStop {
		return failureWithJob("pause", "pause_not_applicable", "after_stop has no semantic waiting state; cancel it instead", current)
	}
	job, _, err := s.store.UpdateJob(args.JobID, "mcp.pause", "", func(job *pluginstate.Job, now time.Time) error {
		if expire(job, now) {
			return nil
		}
		if job.State != pluginstate.StateArmed && job.State != pluginstate.StateHostMonitoring &&
			job.State != pluginstate.StateReadyPendingStop && job.State != pluginstate.StateStopObserved {
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

func (s *Service) cancel(ctx context.Context, raw json.RawMessage) Result {
	var args jobArguments
	if err := decodeArguments(raw, &args); err != nil {
		return failure("cancel", "invalid_arguments", err.Error())
	}
	current, err := s.store.Load(args.JobID)
	if err != nil {
		return failure("cancel", "state_error", "could not load the DoneThen job")
	}
	intentAtRequest := current.State == pluginstate.StateActionIntent
	var cancelResult *actions.CancelResult
	unresolvedPower := pluginstate.HasUnresolvedPowerAction(current)
	if unresolvedPower {
		current, _, err = s.store.UpdateJob(args.JobID, "mcp.cancel.requested", "", func(job *pluginstate.Job, _ time.Time) error {
			if !pluginstate.HasUnresolvedPowerAction(*job) {
				return nil
			}
			job.Generation++
			job.CancelRequested = true
			job.CancelReason = "mcp_cancelled_by_user"
			job.ReasonCode = "power_cancel_requested"
			return nil
		})
		if err != nil {
			return failureWithJob("cancel", "state_error", "could not persist the power cancellation request", current)
		}
		if s.backend == nil {
			return failureWithJob("cancel", "power_backend_unavailable", "a power action may be active but the backend is unavailable; use the CLI cancellation path", current)
		}
		receipt, receiptErr := pluginstate.RecoveryReceipt(current)
		if receiptErr != nil {
			return failureWithJob("cancel", "power_receipt_unavailable", "the unresolved power action has no valid recovery receipt; use the CLI recovery path", current)
		}
		result, cancelErr := s.backend.Cancel(ctx, receipt)
		cancelResult = &result
		if cancelErr != nil && !errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
			return failureWithJob("cancel", "power_cancel_failed", "the power backend did not confirm cancellation; retry immediately", current)
		}
	}
	job, _, err := s.store.UpdateJob(args.JobID, "mcp.cancel", "", func(job *pluginstate.Job, now time.Time) error {
		if job.State == pluginstate.StateCancelled {
			return nil
		}
		if intentAtRequest && pluginstate.HasUnresolvedPowerAction(*job) {
			job.CancelRequested = true
			job.CancelReason = "mcp_cancelled_by_user"
			job.ReasonCode = "cancel_requested_pending_scheduler_settlement"
			job.CancelResult = cancelResult
			return nil
		}
		if job.State.IsTerminal() && !pluginstate.HasUnresolvedPowerAction(*job) {
			return nil
		}
		job.Generation++
		job.State = pluginstate.StateCancelled
		job.ReasonCode = "cancelled_by_user"
		job.CancelRequested = true
		job.CancelReason = "mcp_cancelled_by_user"
		job.CancelResult = cancelResult
		clearCompletion(job)
		return nil
	})
	if err != nil {
		return failure("cancel", "state_error", err.Error())
	}
	if intentAtRequest && pluginstate.HasUnresolvedPowerAction(job) {
		return success("cancel", job, "Cancellation is persisted at the action boundary; the supervisor must settle the unresolved scheduler call before this job becomes terminal")
	}
	if job.State != pluginstate.StateCancelled {
		return success("cancel", job, "Job was already terminal; no power action is active")
	}
	if cancelResult != nil {
		return success("cancel", job, fmt.Sprintf("Job cancelled; the power countdown cancellation scope was %s", cancelResult.Scope))
	}
	return success("cancel", job, "Job cancelled before any power action was scheduled")
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
			"trigger_policy":      job.TriggerPolicy,
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
	result.Structured["trigger_policy"] = job.TriggerPolicy
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
	if job.State.IsActive() && job.State != pluginstate.StateActionIntent && job.State != pluginstate.StateActionScheduled && job.Expired(now) {
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
	job.VerifierPassed = false
	job.VerifierExitCode = nil
	job.HookFingerprintH1 = ""
	job.HookFingerprintH2 = ""
	job.HookFingerprintH3 = ""
	job.HookCompatibility = "not_evaluated"
	job.HostSnapshotReason = ""
	job.HostInstanceID = ""
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
