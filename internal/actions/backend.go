package actions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrNoShutdownInProgress = errors.New("no shutdown is in progress")
var ErrPlatformUnsupported = errors.New("power action is unsupported on this platform")
var ErrPrivilegeUnavailable = errors.New("power helper privilege is unavailable")
var ErrPowerActionConflict = errors.New("another machine power action is unresolved")

const ReceiptSchemaVersion = "2"
const legacyReceiptSchemaVersion = "1"

type CancelScope string

const (
	CancelScopeJob          CancelScope = "job"
	CancelScopeSystemGlobal CancelScope = "system-global"
)

type Capabilities struct {
	Platform           string        `json:"platform"`
	BackendID          string        `json:"backend_id"`
	ExecuteSupported   bool          `json:"execute_supported"`
	CancelScope        CancelScope   `json:"cancel_scope"`
	MinimumDelay       time.Duration `json:"minimum_delay"`
	MaximumDelay       time.Duration `json:"maximum_delay"`
	ReconcileSupported bool          `json:"reconcile_supported"`
	Reason             string        `json:"reason,omitempty"`
}

type PowerRequest struct {
	JobID       string        `json:"job_id"`
	Action      string        `json:"action"`
	Delay       time.Duration `json:"delay"`
	Comment     string        `json:"comment"`
	RequestedAt time.Time     `json:"requested_at"`
}

type Receipt struct {
	SchemaVersion  string      `json:"schema_version"`
	Platform       string      `json:"platform"`
	BackendID      string      `json:"backend_id"`
	BackendVersion string      `json:"backend_version"`
	JobID          string      `json:"job_id"`
	Action         string      `json:"action"`
	RequestedAt    time.Time   `json:"requested_at"`
	ScheduledAt    time.Time   `json:"scheduled_at"`
	Deadline       time.Time   `json:"deadline"`
	ExternalToken  string      `json:"external_token,omitempty"`
	CancelScope    CancelScope `json:"cancel_scope"`
	BootID         string      `json:"boot_id,omitempty"`
	ResultCode     int         `json:"result_code"`
	ResultSummary  string      `json:"result_summary,omitempty"`
	Checksum       string      `json:"checksum,omitempty"`
}

type CancelResult struct {
	Cancelled          bool        `json:"cancelled"`
	NoActionInProgress bool        `json:"no_action_in_progress"`
	Scope              CancelScope `json:"scope"`
	ResultCode         int         `json:"result_code"`
	ResultSummary      string      `json:"result_summary,omitempty"`
}

type ReconcileState string

const (
	ReconcileScheduled  ReconcileState = "scheduled"
	ReconcileUnverified ReconcileState = "execution_unverified"
	ReconcileConfirmed  ReconcileState = "executed_confirmed"
)

type ReconcileResult struct {
	State         ReconcileState `json:"state"`
	CheckedAt     time.Time      `json:"checked_at"`
	CurrentBootID string         `json:"current_boot_id,omitempty"`
	Evidence      string         `json:"evidence,omitempty"`
}

type Backend interface {
	Preflight(ctx context.Context, request PowerRequest) (Capabilities, error)
	Schedule(ctx context.Context, request PowerRequest) (Receipt, error)
	Cancel(ctx context.Context, receipt Receipt) (CancelResult, error)
	Reconcile(ctx context.Context, receipt Receipt) (ReconcileResult, error)
}

type ProcessRunner interface {
	Run(ctx context.Context, executable string, args ...string) (exitCode int, output []byte, err error)
}

func ValidateRequest(request PowerRequest, minimum, maximum time.Duration) error {
	if err := ValidateJobID(request.JobID); err != nil {
		return err
	}
	if request.Action != "shutdown" {
		return fmt.Errorf("unsupported power action %q", request.Action)
	}
	if request.Delay < minimum || request.Delay > maximum || request.Delay%time.Second != 0 {
		return fmt.Errorf("shutdown delay must be between %s and %s in whole seconds", minimum, maximum)
	}
	if request.RequestedAt.IsZero() {
		return errors.New("power request timestamp is required")
	}
	if strings.TrimSpace(request.Comment) == "" || len(request.Comment) > 256 {
		return errors.New("power request comment must contain 1 to 256 bytes")
	}
	return nil
}

func ValidateReceipt(receipt Receipt) error {
	if receipt.SchemaVersion != ReceiptSchemaVersion && receipt.SchemaVersion != legacyReceiptSchemaVersion {
		return fmt.Errorf("unsupported power receipt schema %q", receipt.SchemaVersion)
	}
	if err := ValidateJobID(receipt.JobID); err != nil {
		return err
	}
	if receipt.Platform == "" || receipt.BackendID == "" || receipt.BackendVersion == "" {
		return errors.New("power receipt backend identity is required")
	}
	if receipt.Action != "shutdown" {
		return fmt.Errorf("unsupported receipt action %q", receipt.Action)
	}
	if receipt.RequestedAt.IsZero() || receipt.ScheduledAt.IsZero() || receipt.Deadline.IsZero() {
		return errors.New("power receipt timestamps are required")
	}
	if receipt.Deadline.Before(receipt.ScheduledAt) {
		return errors.New("power receipt deadline precedes scheduling")
	}
	if receipt.CancelScope != CancelScopeJob && receipt.CancelScope != CancelScopeSystemGlobal {
		return fmt.Errorf("invalid cancellation scope %q", receipt.CancelScope)
	}
	if receipt.SchemaVersion == ReceiptSchemaVersion {
		if receipt.Checksum == "" || receipt.Checksum != receiptChecksum(receipt) {
			return errors.New("power receipt checksum is missing or invalid")
		}
	}
	return nil
}

// ValidateReceiptForRequest validates the additional invariants that
// distinguish a successful scheduler receipt from an intent-only recovery
// handle or a receipt returned for different backend capabilities.
func ValidateReceiptForRequest(receipt Receipt, request PowerRequest, capabilities Capabilities) error {
	if err := ValidateReceipt(receipt); err != nil {
		return err
	}
	if err := ValidateRequest(request, capabilities.MinimumDelay, capabilities.MaximumDelay); err != nil {
		return err
	}
	if receipt.JobID != request.JobID || receipt.Action != request.Action ||
		receipt.Platform != capabilities.Platform || receipt.BackendID != capabilities.BackendID ||
		receipt.CancelScope != capabilities.CancelScope {
		return errors.New("power receipt does not match the request or preflight capabilities")
	}
	if !receipt.RequestedAt.Equal(request.RequestedAt.UTC()) {
		return errors.New("power receipt request timestamp changed")
	}
	if receipt.Deadline.Sub(receipt.ScheduledAt) != request.Delay {
		return errors.New("power receipt deadline does not match the requested delay")
	}
	if receipt.ResultCode != 0 {
		return errors.New("power receipt does not record a successful scheduler result")
	}
	if receipt.CancelScope == CancelScopeJob && strings.TrimSpace(receipt.ExternalToken) == "" {
		return errors.New("job-scoped power receipt is missing its cancellation token")
	}
	return nil
}

func SealReceipt(receipt Receipt) Receipt {
	receipt.SchemaVersion = ReceiptSchemaVersion
	receipt.Checksum = ""
	receipt.Checksum = receiptChecksum(receipt)
	return receipt
}

// BuildIntentReceipt creates a recovery handle before an external scheduler is
// called. It is not proof that scheduling succeeded; it exists so a crash at
// the call boundary still leaves cancel/reconcile with a narrow backend token.
func BuildIntentReceipt(jobID, action string, requestedAt time.Time, delay time.Duration, capabilities Capabilities) (Receipt, error) {
	if err := ValidateJobID(jobID); err != nil {
		return Receipt{}, err
	}
	if action != "shutdown" {
		return Receipt{}, fmt.Errorf("unsupported intent action %q", action)
	}
	if requestedAt.IsZero() || delay <= 0 || delay%time.Second != 0 {
		return Receipt{}, errors.New("intent timestamp and whole-second delay are required")
	}
	if capabilities.Platform == "" || capabilities.BackendID == "" {
		return Receipt{}, errors.New("intent backend identity is required")
	}
	if capabilities.CancelScope != CancelScopeJob && capabilities.CancelScope != CancelScopeSystemGlobal {
		return Receipt{}, errors.New("intent cancellation scope is invalid")
	}
	if capabilities.MinimumDelay > 0 && delay < capabilities.MinimumDelay {
		return Receipt{}, errors.New("intent delay is below the backend minimum")
	}
	if capabilities.MaximumDelay > 0 && delay > capabilities.MaximumDelay {
		return Receipt{}, errors.New("intent delay exceeds the backend maximum")
	}
	externalToken := ""
	if capabilities.Platform == "linux-systemd" && capabilities.BackendID == "linux-systemd-helper" {
		externalToken = SystemdUnitToken(jobID)
	}
	requestedAt = requestedAt.UTC()
	return SealReceipt(Receipt{
		Platform:       capabilities.Platform,
		BackendID:      capabilities.BackendID,
		BackendVersion: "intent-recovery-v1",
		JobID:          jobID,
		Action:         action,
		RequestedAt:    requestedAt,
		ScheduledAt:    requestedAt,
		Deadline:       requestedAt.Add(delay),
		ExternalToken:  externalToken,
		CancelScope:    capabilities.CancelScope,
		ResultCode:     -1,
		ResultSummary:  "action intent persisted before external scheduling",
	}), nil
}

func receiptChecksum(receipt Receipt) string {
	receipt.Checksum = ""
	payload, _ := json.Marshal(receipt)
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
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

func SystemdUnitToken(jobID string) string {
	if ValidateJobID(jobID) != nil {
		return ""
	}
	digest := sha256.Sum256([]byte(jobID))
	return "donethen-shutdown-" + hex.EncodeToString(digest[:12])
}
