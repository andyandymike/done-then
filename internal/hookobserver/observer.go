package hookobserver

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/andyandymike/done-then/internal/identity"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

const maxHookInputBytes = 1 << 20

var doneThenTools = map[string]string{
	"mcp__done_then__arm":    "arm",
	"mcp__done_then__finish": "finish",
	"mcp__done_then__pause":  "pause",
	"mcp__done_then__cancel": "cancel",
}

type Observer struct {
	store *pluginstate.Store
}

type hookInput struct {
	SessionID      string          `json:"session_id"`
	TurnID         string          `json:"turn_id"`
	CWD            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	StopHookActive bool            `json:"stop_hook_active"`
}

func New(state *pluginstate.Store) (*Observer, error) {
	if state == nil {
		return nil, errors.New("plugin state store is required")
	}
	return &Observer{store: state}, nil
}

func (o *Observer) Handle(reader io.Reader) error {
	input, err := decodeHookInput(reader)
	if err != nil {
		return err
	}
	switch input.HookEventName {
	case "PostToolUse":
		return o.postToolUse(input)
	case "UserPromptSubmit":
		return o.userPromptSubmit(input)
	case "Stop":
		return o.stop(input)
	case "SessionEnd":
		return o.sessionEnd(input)
	default:
		return fmt.Errorf("unsupported DoneThen hook event %q", input.HookEventName)
	}
}

func (o *Observer) postToolUse(input hookInput) error {
	tool, supported := doneThenTools[input.ToolName]
	if !supported {
		// The matcher is intentionally narrow. If a future host invokes this
		// handler more broadly, stay inert instead of observing unrelated tools.
		return nil
	}
	if err := validateTurnInput(input); err != nil {
		return err
	}
	responseJobID, responseOK, err := responseIdentity(input.ToolResponse)
	if err != nil {
		return fmt.Errorf("inspect %s tool response: %w", tool, err)
	}
	if !responseOK {
		return nil
	}
	jobID := responseJobID
	if tool != "arm" {
		inputJobID, err := inputIdentity(input.ToolInput)
		if err != nil {
			return fmt.Errorf("inspect %s tool input: %w", tool, err)
		}
		if responseJobID != "" && responseJobID != inputJobID {
			return errors.New("DoneThen tool input and output job ids do not match")
		}
		jobID = inputJobID
	}
	if err := pluginstate.ValidateJobID(jobID); err != nil {
		return errors.New("DoneThen tool response is missing a valid job id")
	}
	eventKey := hookEventKey(input, jobID)
	if tool == "arm" {
		_, _, err := o.store.BindSession(jobID, input.SessionID, input.TurnID, input.CWD, eventKey)
		return err
	}
	_, _, err = o.store.UpdateJob(jobID, "hook.post_tool."+tool, eventKey, func(job *pluginstate.Job, now time.Time) error {
		if job.SessionID != input.SessionID {
			if requestPowerCancellation(job, "tool_session_mismatch_after_power_intent") {
				return nil
			}
			if job.State.IsActive() {
				job.Generation++
				job.State = pluginstate.StateHookUnavailable
				job.ReasonCode = "tool_session_mismatch"
				clearCompletion(job)
			}
			return nil
		}
		if job.State.IsActive() && job.Expired(now) {
			job.Generation++
			job.State = pluginstate.StateExpired
			job.ReasonCode = "arm_expired"
			clearCompletion(job)
			return nil
		}
		if tool == "finish" && job.State == pluginstate.StateReadyPendingStop {
			if job.ReadyTurnID == "" || job.ReadyTurnID != input.TurnID {
				job.Generation++
				job.State = pluginstate.StateArmed
				job.ReasonCode = "finish_turn_mismatch"
				job.CurrentTurnID = input.TurnID
				clearCompletion(job)
				return nil
			}
			job.FinishObserved = true
			job.ReasonCode = "finish_hook_observed"
		}
		return nil
	})
	return err
}

func (o *Observer) userPromptSubmit(input hookInput) error {
	if err := validateTurnInput(input); err != nil {
		return err
	}
	eventKey := hookEventKey(input, "")
	_, _, _, err := o.store.UpdateSession(input.SessionID, input.TurnID, "hook.user_prompt", eventKey, func(job *pluginstate.Job, now time.Time) error {
		if job.State.IsTerminal() {
			return nil
		}
		if requestPowerCancellation(job, "new_prompt_requested_power_cancel") {
			return nil
		}
		if job.Expired(now) {
			job.Generation++
			job.State = pluginstate.StateExpired
			job.ReasonCode = "arm_expired"
			clearCompletion(job)
			return nil
		}
		if job.TriggerPolicy == pluginstate.TriggerAfterStop {
			job.Generation++
			job.State = pluginstate.StateCancelled
			job.ReasonCode = "new_prompt_cancelled_after_stop_grant"
			job.CancelRequested = true
			job.CancelReason = "new_prompt_cancelled_after_stop_grant"
			return nil
		}
		job.Generation++
		job.CurrentTurnID = input.TurnID
		job.State = pluginstate.StateArmed
		job.ReasonCode = "new_prompt_invalidated_evidence"
		clearCompletion(job)
		return nil
	})
	return err
}

func (o *Observer) stop(input hookInput) error {
	if err := validateTurnInput(input); err != nil {
		return err
	}
	eventKey := hookEventKey(input, "")
	_, _, _, err := o.store.UpdateSession(input.SessionID, input.TurnID, "hook.stop", eventKey, func(job *pluginstate.Job, now time.Time) error {
		if job.State.IsTerminal() {
			return nil
		}
		if pluginstate.HasUnresolvedPowerAction(*job) {
			if job.TriggerPolicy == pluginstate.TriggerAfterStop || input.TurnID != job.StopTurnID {
				requestPowerCancellation(job, "continuation_after_stop_requested_power_cancel")
			}
			return nil
		}
		if job.Expired(now) {
			job.Generation++
			job.State = pluginstate.StateExpired
			job.ReasonCode = "arm_expired"
			clearCompletion(job)
			return nil
		}
		if job.TriggerPolicy == pluginstate.TriggerAfterStop {
			if input.StopHookActive {
				job.Generation++
				job.State = pluginstate.StateCancelled
				job.ReasonCode = "stop_hook_continuation_cancelled_grant"
				job.CancelRequested = true
				job.CancelReason = "stop_hook_continuation_cancelled_grant"
				return nil
			}
			if !job.DryRun && !pluginstate.WorkspaceMatches(job.WorkspaceCWD, input.CWD) {
				job.Generation++
				job.State = pluginstate.StateHookUnavailable
				job.ReasonCode = "stop_workspace_mismatch"
				return nil
			}
			if job.State == pluginstate.StateStopObserved {
				job.Generation++
				job.State = pluginstate.StateCancelled
				job.ReasonCode = "continuation_after_stop_cancelled_grant"
				job.CancelRequested = true
				job.CancelReason = "continuation_after_stop_cancelled_grant"
				return nil
			}
			if job.State != pluginstate.StateArmed || input.TurnID != job.CurrentTurnID {
				return nil
			}
			job.StopTurnID = input.TurnID
			if job.DryRun {
				job.State = pluginstate.StateDryRunComplete
				job.ReasonCode = "after_stop_observed_no_action"
			} else {
				job.State = pluginstate.StateStopObserved
				job.ReasonCode = "after_stop_observed_awaiting_countdown"
			}
			return nil
		}
		if job.State != pluginstate.StateReadyPendingStop {
			return nil
		}
		if !job.FinishObserved || job.ReadyTurnID != input.TurnID {
			job.Generation++
			job.State = pluginstate.StateArmed
			job.ReasonCode = "stop_turn_mismatch"
			job.CurrentTurnID = input.TurnID
			clearCompletion(job)
			return nil
		}
		job.State = pluginstate.StateStopObserved
		job.StopTurnID = input.TurnID
		if job.DryRun {
			job.ReasonCode = "matching_stop_observed_no_action"
		} else {
			job.ReasonCode = "matching_stop_observed_awaiting_final_gate"
		}
		return nil
	})
	return err
}

func (o *Observer) sessionEnd(input hookInput) error {
	if strings.TrimSpace(input.SessionID) == "" || len(input.SessionID) > 1024 {
		return errors.New("hook input has an invalid session_id")
	}
	eventKey := hookEventKey(input, "")
	_, _, _, err := o.store.UpdateSession(input.SessionID, "", "hook.session_end", eventKey, func(job *pluginstate.Job, _ time.Time) error {
		if job.State.IsTerminal() {
			return nil
		}
		if job.TriggerPolicy == pluginstate.TriggerAfterStop &&
			(job.State == pluginstate.StateStopObserved || pluginstate.HasUnresolvedPowerAction(*job)) {
			// Once the matching Stop has been accepted, closing Codex must not
			// silently revoke the user's explicit after-stop shutdown request.
			return nil
		}
		if requestPowerCancellation(job, "session_end_requested_power_cancel") {
			return nil
		}
		job.Generation++
		job.State = pluginstate.StateExpired
		job.ReasonCode = "session_ended"
		clearCompletion(job)
		return nil
	})
	return err
}

func decodeHookInput(reader io.Reader) (hookInput, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxHookInputBytes+1))
	if err != nil {
		return hookInput{}, fmt.Errorf("read hook input: %w", err)
	}
	if len(data) == 0 {
		return hookInput{}, errors.New("hook input is empty")
	}
	if len(data) > maxHookInputBytes {
		return hookInput{}, fmt.Errorf("hook input exceeds %d bytes", maxHookInputBytes)
	}
	if !utf8.Valid(data) {
		return hookInput{}, errors.New("hook input is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var input hookInput
	if err := decoder.Decode(&input); err != nil {
		return hookInput{}, fmt.Errorf("decode hook input: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return hookInput{}, errors.New("hook input contains trailing JSON")
	}
	if strings.TrimSpace(input.HookEventName) == "" {
		return hookInput{}, errors.New("hook input is missing hook_event_name")
	}
	return input, nil
}

func validateTurnInput(input hookInput) error {
	if strings.TrimSpace(input.SessionID) == "" || len(input.SessionID) > 1024 {
		return errors.New("hook input has an invalid session_id")
	}
	if strings.TrimSpace(input.TurnID) == "" || len(input.TurnID) > 1024 {
		return errors.New("hook input has an invalid turn_id")
	}
	if len(input.ToolUseID) > 1024 {
		return errors.New("hook input has an invalid tool_use_id")
	}
	if len(input.CWD) > 32768 {
		return errors.New("hook input has an invalid cwd")
	}
	return nil
}

func inputIdentity(raw json.RawMessage) (string, error) {
	var input struct {
		JobID string `json:"job_id"`
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", errors.New("tool_input is empty")
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", err
	}
	return input.JobID, nil
}

func responseIdentity(raw json.RawMessage) (string, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false, errors.New("tool_response is empty")
	}
	type responseEnvelope struct {
		IsError           bool            `json:"isError"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		StructuredSnake   json.RawMessage `json:"structured_content"`
		Result            json.RawMessage `json:"result"`
	}
	var envelope responseEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "", false, err
	}
	if len(envelope.StructuredContent) == 0 {
		envelope.StructuredContent = envelope.StructuredSnake
	}
	if len(envelope.StructuredContent) == 0 && len(envelope.Result) != 0 {
		var nested responseEnvelope
		if err := json.Unmarshal(envelope.Result, &nested); err != nil {
			return "", false, err
		}
		envelope = nested
		if len(envelope.StructuredContent) == 0 {
			envelope.StructuredContent = envelope.StructuredSnake
		}
	}
	if envelope.IsError {
		return "", false, nil
	}
	if len(envelope.StructuredContent) == 0 {
		return "", false, errors.New("tool response has no structuredContent")
	}
	var structured struct {
		OK    *bool  `json:"ok"`
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(envelope.StructuredContent, &structured); err != nil {
		return "", false, err
	}
	if structured.OK == nil {
		return "", false, errors.New("tool response has no ok field")
	}
	return structured.JobID, *structured.OK, nil
}

func hookEventKey(input hookInput, jobID string) string {
	value := strings.Join([]string{
		input.SessionID,
		input.TurnID,
		input.HookEventName,
		input.CWD,
		input.ToolUseID,
		jobID,
		strconv.FormatBool(input.StopHookActive),
	}, "\x00")
	return identity.SHA256([]byte(value))
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
	job.CancelRequested = false
	job.CancelReason = ""
}

func requestPowerCancellation(job *pluginstate.Job, reason string) bool {
	if !pluginstate.HasUnresolvedPowerAction(*job) {
		return false
	}
	job.Generation++
	job.CancelRequested = true
	job.CancelReason = reason
	job.ReasonCode = reason
	return true
}
