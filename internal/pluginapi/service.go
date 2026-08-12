package pluginapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

// PolicyCapability separates portable build support, backend-family support,
// the result of the current environment preflight, and the final policy gate.
// A supported OS is deliberately not enough to make power execution ready.
type PolicyCapability struct {
	BuildSupported         bool
	BackendSupported       bool
	BackendPreflightPassed bool
	ExecuteReady           bool
	UnavailableReason      string
}

type Service struct {
	store                  *pluginstate.Store
	now                    func() time.Time
	workspace              string
	profiles               *verifierprofile.Registry
	launcher               JobLauncher
	backend                actions.Backend
	powerPolicyFingerprint string
	allowAgentOnlySuccess  bool
	policyCapabilities     map[pluginstate.TriggerPolicy]PolicyCapability
}

type JobLauncher interface {
	Launch(jobID string) (int, error)
}

type CancelWorkerLauncher interface {
	EnsureCancelWorker(jobID, bindingID, reason string) (int, error)
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
	PolicyCapabilities                map[pluginstate.TriggerPolicy]PolicyCapability
}

func New(state *pluginstate.Store) (*Service, error) {
	return NewWithOptions(state, Options{})
}

func NewWithOptions(state *pluginstate.Store, options Options) (*Service, error) {
	if state == nil {
		return nil, errors.New("plugin state store is required")
	}
	policyCapabilities := normalizedPolicyCapabilities(options)
	if anyExecuteReady(policyCapabilities) {
		if !filepath.IsAbs(options.Workspace) {
			return nil, errors.New("plugin execute requires an absolute workspace")
		}
		if options.Launcher == nil || options.Backend == nil {
			return nil, errors.New("plugin execute requires a launcher and power backend")
		}
	}
	if policyCapabilities[pluginstate.TriggerVerifiedSuccess].ExecuteReady {
		if options.Profiles == nil || options.PowerPolicyFingerprint == "" {
			return nil, errors.New("verified-success execute requires profiles and a fixed power policy")
		}
	}
	return &Service{
		store:                  state,
		now:                    time.Now,
		workspace:              options.Workspace,
		profiles:               options.Profiles,
		launcher:               options.Launcher,
		backend:                options.Backend,
		powerPolicyFingerprint: options.PowerPolicyFingerprint,
		allowAgentOnlySuccess:  options.AllowAgentOnlySuccess,
		policyCapabilities:     policyCapabilities,
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
	afterStop := s.capability(pluginstate.TriggerAfterStop)
	afterAllStop := s.capability(pluginstate.TriggerAfterAllStop)
	verified := s.capability(pluginstate.TriggerVerifiedSuccess)
	result.Structured["execute_available"] = afterStop.ExecuteReady || afterAllStop.ExecuteReady || verified.ExecuteReady
	result.Structured["after_stop_execute_available"] = afterStop.ExecuteReady
	result.Structured["after_all_stop_execute_available"] = afterAllStop.ExecuteReady
	result.Structured["verified_success_execute_available"] = verified.ExecuteReady
	result.Structured["build_supported_by_policy"] = policyCapabilityBools(s.policyCapabilities, func(value PolicyCapability) bool { return value.BuildSupported })
	result.Structured["backend_supported_by_policy"] = policyCapabilityBools(s.policyCapabilities, func(value PolicyCapability) bool { return value.BackendSupported })
	result.Structured["backend_preflight_passed_by_policy"] = policyCapabilityBools(s.policyCapabilities, func(value PolicyCapability) bool { return value.BackendPreflightPassed })
	result.Structured["execute_ready_by_policy"] = policyCapabilityBools(s.policyCapabilities, func(value PolicyCapability) bool { return value.ExecuteReady })
	result.Structured["execute_unavailable_reasons_by_policy"] = policyCapabilityReasons(s.policyCapabilities)
	return result
}

type armArguments struct {
	Action                        string   `json:"action"`
	TriggerPolicy                 string   `json:"trigger_policy"`
	AcknowledgeStopWithoutSuccess bool     `json:"acknowledge_stop_without_success"`
	AcknowledgeBarrierAcrossTurns bool     `json:"acknowledge_barrier_across_turns"`
	TargetSessionIDs              []string `json:"target_session_ids"`
	DelaySeconds                  int64    `json:"delay_seconds"`
	ExpiresInSeconds              int64    `json:"expires_in_seconds"`
	Mode                          string   `json:"mode"`
	VerifierProfile               string   `json:"verifier_profile"`
	AllowAgentOnlySuccess         bool     `json:"allow_agent_only_success"`
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
		return failure("arm", "invalid_trigger_policy", "trigger_policy must be after_stop, after_all_stop, or verified_success")
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
	var stopTargets []pluginstate.StopTarget
	if triggerPolicy == pluginstate.TriggerAfterAllStop {
		if len(args.TargetSessionIDs) < pluginstate.MinimumStopTargets || len(args.TargetSessionIDs) > pluginstate.MaximumStopTargets {
			return failure("arm", "invalid_target_sessions", "after_all_stop requires between 2 and 16 target_session_ids")
		}
		seenRaw := make(map[string]bool, len(args.TargetSessionIDs))
		seenHashes := make(map[string]bool, len(args.TargetSessionIDs))
		stopTargets = make([]pluginstate.StopTarget, 0, len(args.TargetSessionIDs))
		for _, sessionID := range args.TargetSessionIDs {
			if sessionID == "" || len(sessionID) > 1024 || strings.TrimSpace(sessionID) != sessionID {
				return failure("arm", "invalid_target_sessions", "target session ids must be non-empty exact values without leading or trailing whitespace")
			}
			if seenRaw[sessionID] {
				return failure("arm", "duplicate_target_session", "target_session_ids must not contain duplicates")
			}
			seenRaw[sessionID] = true
			sessionHash := identity.SHA256([]byte(sessionID))
			if seenHashes[sessionHash] {
				return failure("arm", "target_identity_collision", "target session identities collide after hashing")
			}
			seenHashes[sessionHash] = true
			stopTargets = append(stopTargets, pluginstate.StopTarget{SessionHash: sessionHash})
		}
	} else if len(args.TargetSessionIDs) != 0 || args.AcknowledgeBarrierAcrossTurns {
		return failure("arm", "barrier_arguments_not_applicable", "target_session_ids and acknowledge_barrier_across_turns are only valid for after_all_stop")
	}
	if triggerPolicy == pluginstate.TriggerAfterStop || triggerPolicy == pluginstate.TriggerAfterAllStop {
		if args.VerifierProfile == "" {
			args.VerifierProfile = "none"
		}
		if args.VerifierProfile != "none" || args.AllowAgentOnlySuccess {
			return failure("arm", "semantic_verification_not_applicable", "observable Stop policies must use verifier_profile=none and allow_agent_only_success=false")
		}
	} else if args.AcknowledgeStopWithoutSuccess {
		return failure("arm", "invalid_acknowledgement", "verified_success cannot acknowledge after-stop semantics")
	}
	if args.Mode != "dry_run" {
		if args.Mode != "execute" {
			return failure("arm", "invalid_mode", "mode must be dry_run or execute")
		}
		capability := s.capability(triggerPolicy)
		if !capability.ExecuteReady {
			reason := capability.UnavailableReason
			if reason == "" {
				if triggerPolicy != pluginstate.TriggerVerifiedSuccess {
					reason = "observable Stop execute is unavailable on this platform"
				} else {
					reason = "verified-success execute requires validated local policy and same-host authority"
				}
			}
			code := "execute_unavailable"
			if strings.HasPrefix(reason, "stop_arbitration_unavailable") {
				code = "stop_arbitration_unavailable"
			}
			return failure("arm", code, reason)
		}
		if (triggerPolicy == pluginstate.TriggerAfterStop || triggerPolicy == pluginstate.TriggerAfterAllStop) && !args.AcknowledgeStopWithoutSuccess {
			return failure("arm", "stop_without_success_acknowledgement_required", "observable Stop execute requires explicit acknowledgement that a normal Stop can trigger shutdown regardless of task success")
		}
		if triggerPolicy == pluginstate.TriggerAfterAllStop && !args.AcknowledgeBarrierAcrossTurns {
			return failure("arm", "barrier_across_turns_acknowledgement_required", "after_all_stop execute requires acknowledgement that a target which resumes before countdown remains in the barrier until a later Stop")
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
	if (triggerPolicy == pluginstate.TriggerAfterStop || triggerPolicy == pluginstate.TriggerAfterAllStop) && args.Mode == "execute" {
		request := actions.PowerRequest{
			JobID: jobIdentity.JobID, Action: args.Action, Delay: time.Duration(args.DelaySeconds) * time.Second,
			Comment: actions.AfterStopPowerComment(jobIdentity.JobID), RequestedAt: now,
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
	targetBindingID := ""
	if triggerPolicy == pluginstate.TriggerAfterAllStop {
		bindingIdentity, bindingErr := identity.New()
		if bindingErr != nil {
			return failure("arm", "state_error", "could not create a barrier binding identity")
		}
		targetBindingID = bindingIdentity.JobID
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
		StopTargets:            stopTargets,
		TargetBindingID:        targetBindingID,
		BarrierAcrossTurnsAck:  args.AcknowledgeBarrierAcrossTurns,
	}
	if triggerPolicy == pluginstate.TriggerAfterAllStop {
		job, err = s.store.CreateBarrierReservations(job, args.TargetSessionIDs)
	} else {
		err = s.store.Create(job)
	}
	if err != nil {
		if errors.Is(err, pluginstate.ErrTargetReservationConflict) {
			return failure("arm", "target_session_conflict", "at least one target session already belongs to an active DoneThen job")
		}
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
		if triggerPolicy == pluginstate.TriggerAfterAllStop {
			return success("arm", job, "Multi-session Stop barrier reserved; shutdown remains impossible until the controller hook binds and every target has stopped")
		}
		return success("arm", job, "Verified-success job armed; the supervisor is waiting for hook binding and host evidence")
	}
	if triggerPolicy == pluginstate.TriggerAfterStop {
		return success("arm", job, "After-stop dry-run armed; the normal matching Stop will be recorded without scheduling power")
	}
	if triggerPolicy == pluginstate.TriggerAfterAllStop {
		return success("arm", job, "Multi-session Stop barrier dry-run reserved; waiting for controller binding and every target Stop")
	}
	return success("arm", job, "Verified-success dry-run armed; waiting for completion evidence")
}

func normalizedPolicyCapabilities(options Options) map[pluginstate.TriggerPolicy]PolicyCapability {
	capabilities := make(map[pluginstate.TriggerPolicy]PolicyCapability, 3)
	for _, policy := range []pluginstate.TriggerPolicy{
		pluginstate.TriggerAfterStop,
		pluginstate.TriggerAfterAllStop,
		pluginstate.TriggerVerifiedSuccess,
	} {
		if value, found := options.PolicyCapabilities[policy]; found {
			capabilities[policy] = value
		}
	}
	// Preserve the old constructor surface for in-package tests and embedders.
	// Production startup uses PolicyCapabilities so GOOS alone never grants
	// authority. Both Stop policies intentionally share the legacy value.
	if _, found := options.PolicyCapabilities[pluginstate.TriggerAfterStop]; !found {
		capabilities[pluginstate.TriggerAfterStop] = PolicyCapability{
			BuildSupported:         options.AfterStopExecuteAvailable,
			BackendSupported:       options.AfterStopExecuteAvailable,
			BackendPreflightPassed: options.AfterStopExecuteAvailable,
			ExecuteReady:           options.AfterStopExecuteAvailable,
			UnavailableReason:      options.AfterStopExecuteUnavailableReason,
		}
	}
	if _, found := options.PolicyCapabilities[pluginstate.TriggerAfterAllStop]; !found {
		capabilities[pluginstate.TriggerAfterAllStop] = capabilities[pluginstate.TriggerAfterStop]
	}
	if _, found := options.PolicyCapabilities[pluginstate.TriggerVerifiedSuccess]; !found {
		capabilities[pluginstate.TriggerVerifiedSuccess] = PolicyCapability{
			BuildSupported:         options.ExecuteAvailable,
			BackendSupported:       options.ExecuteAvailable,
			BackendPreflightPassed: options.ExecuteAvailable,
			ExecuteReady:           options.ExecuteAvailable,
			UnavailableReason:      options.ExecuteUnavailableReason,
		}
	}
	return capabilities
}

func anyExecuteReady(capabilities map[pluginstate.TriggerPolicy]PolicyCapability) bool {
	for _, capability := range capabilities {
		if capability.ExecuteReady {
			return true
		}
	}
	return false
}

func (s *Service) capability(policy pluginstate.TriggerPolicy) PolicyCapability {
	return s.policyCapabilities[policy]
}

func policyCapabilityBools(capabilities map[pluginstate.TriggerPolicy]PolicyCapability, selectValue func(PolicyCapability) bool) map[string]bool {
	result := make(map[string]bool, len(capabilities))
	for policy, capability := range capabilities {
		result[string(policy)] = selectValue(capability)
	}
	return result
}

func policyCapabilityReasons(capabilities map[pluginstate.TriggerPolicy]PolicyCapability) map[string]string {
	result := make(map[string]string, len(capabilities))
	for policy, capability := range capabilities {
		result[string(policy)] = capability.UnavailableReason
	}
	return result
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
	if current.TriggerPolicy != pluginstate.TriggerVerifiedSuccess {
		return failureWithJob("finish", "finish_not_required", "observable Stop policies are driven only by Stop hooks; semantic completion is not evaluated", current)
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
	if current.TriggerPolicy != pluginstate.TriggerVerifiedSuccess {
		return failureWithJob("pause", "pause_not_applicable", "observable Stop policies have no semantic waiting state; cancel them instead", current)
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
		authority, recoveryErr := s.store.LoadRecoveryAuthority(current.JobID)
		if recoveryErr == nil {
			if authority.Resolution != nil {
				cancelResult = authority.Resolution.CancelResult
			} else {
				launcher, ok := s.launcher.(CancelWorkerLauncher)
				if !ok || launcher == nil {
					return failureWithJob("cancel", "cancel_worker_unavailable", "the cancellation request is durable, but this service cannot start the machine-fenced recovery worker; use the CLI cancellation path", current)
				}
				if _, launchErr := launcher.EnsureCancelWorker(current.JobID, pluginstate.ObservedBindingID(current), "mcp_cancelled_by_user"); launchErr != nil {
					return failureWithJob("cancel", "cancel_worker_unavailable", "the cancellation request is durable, but the machine-fenced recovery worker could not start; use the CLI cancellation path", current)
				}
				return success("cancel", current, "Cancellation is durable; a machine-fenced cancel-only worker is settling the scheduler boundary")
			}
		} else {
			if !errors.Is(recoveryErr, os.ErrNotExist) {
				return failureWithJob("cancel", "recovery_authority_unavailable", "the cancellation request is durable, but independent recovery authority is unreadable; use the CLI cancellation path", current)
			}
			// Legacy unresolved jobs predate the independent call-start record.
			// Their phase is unknowable, so a receipt-bound cancellation is safer
			// than assuming the backend was never called.
			if s.backend == nil {
				return failureWithJob("cancel", "power_backend_unavailable", "a legacy power action may be active but the backend is unavailable; use the CLI cancellation path", current)
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
	}
	job, _, err := s.store.UpdateJob(args.JobID, "mcp.cancel", "", func(job *pluginstate.Job, now time.Time) error {
		if job.State == pluginstate.StateCancelled {
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
			if authority, recoveryErr := s.store.LoadRecoveryAuthority(args.JobID); recoveryErr == nil {
				return Result{
					Text: "DoneThen recovery status returned; the mutable job projection is unavailable",
					Structured: map[string]any{
						"ok": true, "tool": "status", "recovery": authority.Status(),
						"job_projection_available": false, "execute_available": false,
					},
				}
			}
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
