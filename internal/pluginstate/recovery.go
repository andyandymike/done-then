package pluginstate

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/filetrust"
	"github.com/andyandymike/done-then/internal/identity"
)

const recoveryRecordSchema = "1"
const maxRecoveryRecordBytes = 64 * 1024

// RecoveryEnvelope is written before any schedule call can start. It contains
// only fixed, allowlisted cancellation authority and no prompt/session data.
type RecoveryEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	JobID         string          `json:"job_id"`
	BindingID     string          `json:"binding_id"`
	Action        string          `json:"action"`
	TriggerPolicy TriggerPolicy   `json:"trigger_policy"`
	CreatedAt     time.Time       `json:"created_at"`
	IntentReceipt actions.Receipt `json:"intent_receipt"`
	EnvelopeHash  string          `json:"envelope_hash"`
}

// ScheduleCallStarted is the durable no-retry boundary. Its presence means a
// process may have entered the backend call even when no outcome was recorded.
type ScheduleCallStarted struct {
	SchemaVersion string    `json:"schema_version"`
	JobID         string    `json:"job_id"`
	EnvelopeHash  string    `json:"envelope_hash"`
	StartedAt     time.Time `json:"started_at"`
	CallHash      string    `json:"call_hash"`
}

// ScheduleReceiptSeal preserves the backend-returned receipt independently of
// the mutable job projection.
type ScheduleReceiptSeal struct {
	SchemaVersion string          `json:"schema_version"`
	JobID         string          `json:"job_id"`
	EnvelopeHash  string          `json:"envelope_hash"`
	CallHash      string          `json:"call_hash"`
	SealedAt      time.Time       `json:"sealed_at"`
	Receipt       actions.Receipt `json:"receipt"`
	SealHash      string          `json:"seal_hash"`
}

// RecoveryResolution is written only after positive evidence that the
// external action is inert. It remains useful even if updating job.json fails.
type RecoveryResolution struct {
	SchemaVersion  string                `json:"schema_version"`
	JobID          string                `json:"job_id"`
	EnvelopeHash   string                `json:"envelope_hash"`
	CallHash       string                `json:"call_hash,omitempty"`
	ReceiptHash    string                `json:"receipt_hash,omitempty"`
	Outcome        string                `json:"outcome"`
	Reason         string                `json:"reason"`
	ResolvedAt     time.Time             `json:"resolved_at"`
	CancelResult   *actions.CancelResult `json:"cancel_result,omitempty"`
	ResolutionHash string                `json:"resolution_hash"`
}

type RecoveryAuthority struct {
	Envelope   RecoveryEnvelope
	Call       *ScheduleCallStarted
	Receipt    *ScheduleReceiptSeal
	Resolution *RecoveryResolution
}

type RecoveryStatus struct {
	JobID                string `json:"job_id"`
	Phase                string `json:"phase"`
	ReceiptSealed        bool   `json:"receipt_sealed"`
	RequiresSettlement   bool   `json:"requires_settlement"`
	RequiresCancellation bool   `json:"requires_cancellation"`
	Resolution           string `json:"resolution,omitempty"`
	CancelScope          string `json:"cancel_scope"`
	CancelCommand        string `json:"cancel_command"`
	ReconcileCommand     string `json:"reconcile_command"`
}

func (a RecoveryAuthority) RequiresCancellation() bool {
	return a.Call != nil && a.Resolution == nil
}

func (a RecoveryAuthority) CancellationReceipt() actions.Receipt {
	if a.Receipt != nil {
		return a.Receipt.Receipt
	}
	return a.Envelope.IntentReceipt
}

func (a RecoveryAuthority) Status() RecoveryStatus {
	phase := "INTENT_PERSISTED_NO_CALL"
	if a.Call != nil {
		phase = "SCHEDULE_CALL_STARTED_OUTCOME_UNKNOWN"
	}
	if a.Receipt != nil {
		phase = "SCHEDULE_RECEIPT_SEALED"
	}
	resolution := ""
	if a.Resolution != nil {
		phase = "RESOLVED"
		resolution = a.Resolution.Outcome
	}
	return RecoveryStatus{
		JobID: a.Envelope.JobID, Phase: phase, ReceiptSealed: a.Receipt != nil,
		RequiresSettlement: a.Resolution == nil, RequiresCancellation: a.RequiresCancellation(), Resolution: resolution,
		CancelScope:   string(a.CancellationReceipt().CancelScope),
		CancelCommand: "donethen cancel " + a.Envelope.JobID, ReconcileCommand: "donethen reconcile " + a.Envelope.JobID,
	}
}

func (s *Store) PersistRecoveryEnvelope(job Job, receipt actions.Receipt, createdAt time.Time) (RecoveryEnvelope, error) {
	if err := Validate(job); err != nil {
		return RecoveryEnvelope{}, err
	}
	if job.DryRun || job.State != StateStopObserved {
		return RecoveryEnvelope{}, errors.New("recovery envelope requires an executable stop-observed job")
	}
	if err := actions.ValidateReceipt(receipt); err != nil || receipt.JobID != job.JobID || receipt.Action != job.Action {
		return RecoveryEnvelope{}, errors.New("recovery envelope receipt does not match the job")
	}
	record := RecoveryEnvelope{
		SchemaVersion: recoveryRecordSchema, JobID: job.JobID, BindingID: ObservedBindingID(job), Action: job.Action,
		TriggerPolicy: job.TriggerPolicy, CreatedAt: createdAt.UTC(), IntentReceipt: receipt,
	}
	record.EnvelopeHash = hashRecoveryEnvelope(record)
	if err := validateRecoveryEnvelope(record); err != nil {
		return RecoveryEnvelope{}, err
	}
	if err := s.writeRecoveryCreateOnce(s.recoveryEnvelopePath(job.JobID), record); err != nil {
		return RecoveryEnvelope{}, err
	}
	return record, nil
}

func (s *Store) PersistScheduleCallStarted(jobID, envelopeHash string, startedAt time.Time) (ScheduleCallStarted, error) {
	if ValidateJobID(jobID) != nil || !ValidIdentityHash(envelopeHash) || startedAt.IsZero() {
		return ScheduleCallStarted{}, errors.New("invalid schedule-call-start record")
	}
	authority, err := s.LoadRecoveryAuthority(jobID)
	if err != nil {
		return ScheduleCallStarted{}, err
	}
	if authority.Envelope.EnvelopeHash != envelopeHash {
		return ScheduleCallStarted{}, errors.New("schedule-call-start envelope mismatch")
	}
	if authority.Resolution != nil {
		return ScheduleCallStarted{}, errors.New("resolved recovery authority cannot start a schedule call")
	}
	if authority.Call != nil {
		return ScheduleCallStarted{}, errors.New("schedule call has already crossed the no-retry boundary")
	}
	record := ScheduleCallStarted{
		SchemaVersion: recoveryRecordSchema, JobID: jobID, EnvelopeHash: envelopeHash,
		StartedAt: startedAt.UTC(),
	}
	record.CallHash = hashScheduleCall(record)
	if err := validateScheduleCall(record, authority.Envelope); err != nil {
		return ScheduleCallStarted{}, err
	}
	if err := s.writeRecoveryCreateOnce(s.scheduleCallPath(jobID), record); err != nil {
		return ScheduleCallStarted{}, err
	}
	return record, nil
}

func (s *Store) PersistScheduleReceipt(jobID, envelopeHash, callHash string, receipt actions.Receipt, sealedAt time.Time) (ScheduleReceiptSeal, error) {
	if ValidateJobID(jobID) != nil || !ValidIdentityHash(envelopeHash) || !ValidIdentityHash(callHash) || sealedAt.IsZero() {
		return ScheduleReceiptSeal{}, errors.New("invalid schedule receipt seal identity")
	}
	if err := actions.ValidateReceipt(receipt); err != nil || receipt.JobID != jobID {
		return ScheduleReceiptSeal{}, errors.New("invalid schedule receipt seal receipt")
	}
	authority, err := s.LoadRecoveryAuthority(jobID)
	if err != nil {
		return ScheduleReceiptSeal{}, err
	}
	if authority.Resolution != nil || authority.Call == nil || authority.Envelope.EnvelopeHash != envelopeHash ||
		authority.Call.CallHash != callHash {
		return ScheduleReceiptSeal{}, errors.New("schedule receipt does not match unresolved call authority")
	}
	record := ScheduleReceiptSeal{
		SchemaVersion: recoveryRecordSchema, JobID: jobID, EnvelopeHash: envelopeHash,
		CallHash: callHash, SealedAt: sealedAt.UTC(), Receipt: receipt,
	}
	record.SealHash = hashScheduleReceipt(record)
	if err := validateScheduleReceipt(record, authority.Envelope, *authority.Call); err != nil {
		return ScheduleReceiptSeal{}, err
	}
	if err := s.writeRecoveryCreateOnce(s.scheduleReceiptPath(jobID), record); err != nil {
		return ScheduleReceiptSeal{}, err
	}
	return record, nil
}

func (s *Store) PersistRecoveryResolution(jobID, outcome, reason string, result *actions.CancelResult, resolvedAt time.Time) (RecoveryResolution, error) {
	authority, err := s.LoadRecoveryAuthority(jobID)
	if err != nil {
		return RecoveryResolution{}, err
	}
	if authority.Resolution != nil {
		return *authority.Resolution, nil
	}
	if outcome != "cancelled" && outcome != "no_action" {
		return RecoveryResolution{}, errors.New("invalid recovery resolution outcome")
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 256 || resolvedAt.IsZero() {
		return RecoveryResolution{}, errors.New("invalid recovery resolution reason")
	}
	callHash := ""
	if authority.Call != nil {
		callHash = authority.Call.CallHash
	}
	receiptHash := ""
	if authority.Receipt != nil {
		receiptHash = authority.Receipt.SealHash
	}
	record := RecoveryResolution{
		SchemaVersion: recoveryRecordSchema, JobID: jobID, EnvelopeHash: authority.Envelope.EnvelopeHash,
		CallHash: callHash, ReceiptHash: receiptHash, Outcome: outcome, Reason: reason,
		ResolvedAt: resolvedAt.UTC(), CancelResult: result,
	}
	record.ResolutionHash = hashRecoveryResolution(record)
	if err := validateRecoveryResolution(record, authority); err != nil {
		return RecoveryResolution{}, err
	}
	if err := s.writeRecoveryCreateOnce(s.recoveryResolutionPath(jobID), record); err != nil {
		return RecoveryResolution{}, err
	}
	return record, nil
}

func (s *Store) LoadRecoveryAuthority(jobID string) (RecoveryAuthority, error) {
	if err := ValidateJobID(jobID); err != nil {
		return RecoveryAuthority{}, err
	}
	var authority RecoveryAuthority
	if err := s.readRecoveryRecord(s.recoveryEnvelopePath(jobID), &authority.Envelope); err != nil {
		return RecoveryAuthority{}, err
	}
	if err := validateRecoveryEnvelope(authority.Envelope); err != nil {
		return RecoveryAuthority{}, err
	}
	var call ScheduleCallStarted
	if err := s.readRecoveryRecord(s.scheduleCallPath(jobID), &call); err == nil {
		if err := validateScheduleCall(call, authority.Envelope); err != nil {
			return RecoveryAuthority{}, err
		}
		authority.Call = &call
	} else if !errors.Is(err, os.ErrNotExist) {
		return RecoveryAuthority{}, err
	}
	var receipt ScheduleReceiptSeal
	if err := s.readRecoveryRecord(s.scheduleReceiptPath(jobID), &receipt); err == nil {
		if authority.Call == nil {
			return RecoveryAuthority{}, errors.New("schedule receipt exists without a call-start record")
		}
		if err := validateScheduleReceipt(receipt, authority.Envelope, *authority.Call); err != nil {
			return RecoveryAuthority{}, err
		}
		authority.Receipt = &receipt
	} else if !errors.Is(err, os.ErrNotExist) {
		return RecoveryAuthority{}, err
	}
	var resolution RecoveryResolution
	if err := s.readRecoveryRecord(s.recoveryResolutionPath(jobID), &resolution); err == nil {
		if err := validateRecoveryResolution(resolution, authority); err != nil {
			return RecoveryAuthority{}, err
		}
		authority.Resolution = &resolution
	} else if !errors.Is(err, os.ErrNotExist) {
		return RecoveryAuthority{}, err
	}
	return authority, nil
}

func validateRecoveryEnvelope(record RecoveryEnvelope) error {
	if record.SchemaVersion != recoveryRecordSchema || ValidateJobID(record.JobID) != nil ||
		ValidateJobID(record.BindingID) != nil ||
		record.Action != "shutdown" || !record.TriggerPolicy.Valid() || record.CreatedAt.IsZero() ||
		!ValidIdentityHash(record.EnvelopeHash) || hashRecoveryEnvelope(record) != record.EnvelopeHash {
		return errors.New("invalid recovery envelope")
	}
	if err := actions.ValidateReceipt(record.IntentReceipt); err != nil ||
		record.IntentReceipt.JobID != record.JobID || record.IntentReceipt.Action != record.Action ||
		record.IntentReceipt.BackendVersion != "intent-recovery-v1" || record.IntentReceipt.ResultCode != -1 ||
		!record.IntentReceipt.RequestedAt.Equal(record.CreatedAt) || !record.IntentReceipt.ScheduledAt.Equal(record.CreatedAt) {
		return errors.New("invalid recovery envelope receipt")
	}
	return nil
}

func validateScheduleCall(record ScheduleCallStarted, envelope RecoveryEnvelope) error {
	if record.SchemaVersion != recoveryRecordSchema || record.JobID != envelope.JobID ||
		record.EnvelopeHash != envelope.EnvelopeHash || record.StartedAt.IsZero() ||
		!ValidIdentityHash(record.CallHash) || hashScheduleCall(record) != record.CallHash {
		return errors.New("invalid schedule-call-start record")
	}
	return nil
}

func validateScheduleReceipt(record ScheduleReceiptSeal, envelope RecoveryEnvelope, call ScheduleCallStarted) error {
	if record.SchemaVersion != recoveryRecordSchema || record.JobID != envelope.JobID ||
		record.EnvelopeHash != envelope.EnvelopeHash || record.CallHash != call.CallHash || record.SealedAt.IsZero() ||
		!ValidIdentityHash(record.SealHash) || hashScheduleReceipt(record) != record.SealHash {
		return errors.New("invalid schedule receipt seal")
	}
	if err := actions.ValidateReceipt(record.Receipt); err != nil || record.Receipt.JobID != record.JobID ||
		record.Receipt.Action != envelope.Action {
		return errors.New("invalid sealed schedule receipt")
	}
	return nil
}

func validateRecoveryResolution(record RecoveryResolution, authority RecoveryAuthority) error {
	if record.SchemaVersion != recoveryRecordSchema || record.JobID != authority.Envelope.JobID ||
		record.EnvelopeHash != authority.Envelope.EnvelopeHash || record.ResolvedAt.IsZero() ||
		strings.TrimSpace(record.Reason) == "" || !ValidIdentityHash(record.ResolutionHash) ||
		hashRecoveryResolution(record) != record.ResolutionHash {
		return errors.New("invalid recovery resolution")
	}
	if record.Outcome != "cancelled" && record.Outcome != "no_action" {
		return errors.New("invalid recovery resolution outcome")
	}
	if authority.Call == nil {
		if record.CallHash != "" || record.ReceiptHash != "" || record.Outcome != "no_action" || record.CancelResult != nil {
			return errors.New("pre-call recovery resolution is inconsistent")
		}
	} else {
		if record.CallHash != authority.Call.CallHash {
			return errors.New("recovery resolution call binding mismatch")
		}
		if record.Outcome != "cancelled" {
			return errors.New("a started schedule call requires positive cancellation evidence")
		}
	}
	if authority.Receipt == nil {
		if record.ReceiptHash != "" {
			return errors.New("recovery resolution unexpectedly binds a receipt")
		}
	} else if record.ReceiptHash != authority.Receipt.SealHash {
		return errors.New("recovery resolution receipt binding mismatch")
	}
	if record.Outcome == "cancelled" {
		if record.CancelResult == nil || (!record.CancelResult.Cancelled && !record.CancelResult.NoActionInProgress) ||
			record.CancelResult.Scope != authority.CancellationReceipt().CancelScope {
			return errors.New("cancelled recovery resolution lacks positive inert evidence")
		}
	} else if record.CancelResult != nil {
		return errors.New("non-cancel recovery resolution unexpectedly contains a cancel result")
	}
	return nil
}

func hashRecoveryEnvelope(record RecoveryEnvelope) string {
	record.EnvelopeHash = ""
	return hashRecoveryRecord("recovery-envelope", record)
}

func hashScheduleCall(record ScheduleCallStarted) string {
	record.CallHash = ""
	return hashRecoveryRecord("schedule-call-started", record)
}

func hashScheduleReceipt(record ScheduleReceiptSeal) string {
	record.SealHash = ""
	return hashRecoveryRecord("schedule-receipt-seal", record)
}

func hashRecoveryResolution(record RecoveryResolution) string {
	record.ResolutionHash = ""
	return hashRecoveryRecord("recovery-resolution", record)
}

func hashRecoveryRecord(domain string, record any) string {
	payload, err := json.Marshal(record)
	if err != nil {
		return ""
	}
	framed := append([]byte("donethen:plugin-recovery:"+domain+":"+recoveryRecordSchema+"\x00"), payload...)
	return identity.SHA256(framed)
}

func (s *Store) writeRecoveryCreateOnce(path string, record any) (returnedErr error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	if err := filetrust.EnsureOwnerControlledDirectory(filepath.Dir(path), "plugin recovery job directory"); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, readErr := s.readRecoveryBytes(path)
		if readErr != nil {
			return readErr
		}
		if bytes.Equal(existing, payload) {
			return nil
		}
		return errors.New("recovery record already exists with different content")
	}
	if err != nil {
		return fmt.Errorf("create recovery record: %w", err)
	}
	defer func() {
		_ = file.Close()
		if returnedErr != nil {
			_ = os.Remove(path)
		}
	}()
	if err := filetrust.HardenOwnerControlled(path); err != nil {
		return err
	}
	if _, err := file.Write(payload); err != nil {
		return fmt.Errorf("write recovery record: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush recovery record: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close recovery record: %w", err)
	}
	return nil
}

func (s *Store) readRecoveryRecord(path string, destination any) error {
	payload, err := s.readRecoveryBytes(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode recovery record: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("recovery record contains trailing data")
	}
	return nil
}

func (s *Store) readRecoveryBytes(path string) ([]byte, error) {
	file, info, err := filetrust.OpenOwnerControlled(path, "plugin recovery record")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() < 1 || info.Size() > maxRecoveryRecordBytes {
		return nil, errors.New("plugin recovery record has an invalid size")
	}
	payload, err := io.ReadAll(io.LimitReader(file, maxRecoveryRecordBytes+1))
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func (s *Store) recoveryJobDir(jobID string) string {
	return filepath.Join(s.recoveryDir(), jobID)
}

func (s *Store) recoveryEnvelopePath(jobID string) string {
	return filepath.Join(s.recoveryJobDir(jobID), "envelope.json")
}

func (s *Store) scheduleCallPath(jobID string) string {
	return filepath.Join(s.recoveryJobDir(jobID), "schedule-call-started.json")
}

func (s *Store) scheduleReceiptPath(jobID string) string {
	return filepath.Join(s.recoveryJobDir(jobID), "schedule-receipt.json")
}

func (s *Store) recoveryResolutionPath(jobID string) string {
	return filepath.Join(s.recoveryJobDir(jobID), "resolution.json")
}
