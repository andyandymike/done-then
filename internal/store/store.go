package store

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/filetrust"
	"github.com/andyandymike/done-then/internal/supervisor"
)

const maxRecordBytes = 1 << 20

type Store struct {
	root  string
	now   func() time.Time
	logMu sync.Mutex
}

func DefaultRoot() (string, error) {
	return platformDefaultRoot()
}

func New(root string) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("store root is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve store root: %w", err)
	}
	store := &Store{root: absolute, now: time.Now}
	directories := []struct {
		path  string
		label string
	}{
		{store.root, "DoneThen data root"},
		{store.jobsDir(), "DoneThen job directory"},
		{store.logsDir(), "DoneThen log directory"},
		{store.tmpDir(), "DoneThen temporary directory"},
	}
	for _, directory := range directories {
		if err := filetrust.EnsureOwnerControlledDirectory(directory.path, directory.label); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) Create(job supervisor.Job) error {
	if err := migrateJob(&job); err != nil {
		return err
	}
	if err := validateJobID(job.JobID); err != nil {
		return err
	}
	if err := validateJob(job); err != nil {
		return err
	}
	path := s.jobPath(job.JobID)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("job %s already exists", job.JobID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect job record: %w", err)
	}
	return atomicWriteJSON(path, job)
}

func (s *Store) Save(job supervisor.Job) error {
	if err := migrateJob(&job); err != nil {
		return err
	}
	if err := validateJobID(job.JobID); err != nil {
		return err
	}
	if err := validateJob(job); err != nil {
		return err
	}
	if _, err := os.Lstat(s.jobPath(job.JobID)); err != nil {
		return fmt.Errorf("job record does not exist: %w", err)
	}
	return atomicWriteJSON(s.jobPath(job.JobID), job)
}

func (s *Store) Load(jobID string) (supervisor.Job, error) {
	if err := validateJobID(jobID); err != nil {
		return supervisor.Job{}, err
	}
	file, info, err := filetrust.OpenOwnerControlled(s.jobPath(jobID), "job "+jobID)
	if err != nil {
		return supervisor.Job{}, err
	}
	defer file.Close()
	if info.Size() > maxRecordBytes {
		return supervisor.Job{}, fmt.Errorf("job %s exceeds %d bytes", jobID, maxRecordBytes)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxRecordBytes+1))
	decoder.DisallowUnknownFields()
	var job supervisor.Job
	if err := decoder.Decode(&job); err != nil {
		return supervisor.Job{}, fmt.Errorf("decode job %s: %w", jobID, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return supervisor.Job{}, fmt.Errorf("job %s contains trailing data", jobID)
	}
	if job.JobID != jobID {
		return supervisor.Job{}, fmt.Errorf("job %s has an invalid identity or schema", jobID)
	}
	if err := migrateJob(&job); err != nil {
		return supervisor.Job{}, fmt.Errorf("job %s cannot be migrated: %w", jobID, err)
	}
	if err := validateJob(job); err != nil {
		return supervisor.Job{}, fmt.Errorf("job %s is invalid: %w", jobID, err)
	}
	return job, nil
}

func (s *Store) List() ([]supervisor.Job, error) {
	entries, err := os.ReadDir(s.jobsDir())
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	jobs := make([]supervisor.Job, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		jobID := strings.TrimSuffix(entry.Name(), ".json")
		job, err := s.Load(jobID)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (s *Store) RequestCancel(jobID string) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	if _, err := s.Load(jobID); err != nil {
		return err
	}
	payload := struct {
		RequestedAt time.Time `json:"requested_at"`
	}{RequestedAt: s.now().UTC()}
	return atomicWriteJSON(s.cancelPath(jobID), payload)
}

func (s *Store) Cancelled(jobID string) (bool, error) {
	if err := validateJobID(jobID); err != nil {
		return false, err
	}
	_, err := os.Lstat(s.cancelPath(jobID))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("inspect cancellation request: %w", err)
}

func (s *Store) AppendEvent(event supervisor.Event) error {
	if err := validateJobID(event.JobID); err != nil {
		return err
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	file, err := filetrust.OpenAppendOwnerControlled(s.logPath(event.JobID), "job event log")
	if err != nil {
		return fmt.Errorf("open job event log: %w", err)
	}
	writer := bufio.NewWriter(file)
	encodeErr := json.NewEncoder(writer).Encode(event)
	flushErr := writer.Flush()
	syncErr := file.Sync()
	closeErr := file.Close()
	for _, candidate := range []error{encodeErr, flushErr, syncErr, closeErr} {
		if candidate != nil {
			return fmt.Errorf("write job event log: %w", candidate)
		}
	}
	return nil
}

func (s *Store) ArtifactDir(jobID string) (string, error) {
	if err := validateJobID(jobID); err != nil {
		return "", err
	}
	path := filepath.Join(s.tmpDir(), jobID)
	if err := filetrust.EnsureOwnerControlledDirectory(path, "job artifact directory"); err != nil {
		return "", err
	}
	return path, nil
}

func (s *Store) RemoveArtifacts(jobID string) error {
	if err := validateJobID(jobID); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.tmpDir(), jobID))
}

func (s *Store) ActivePowerJobs() ([]supervisor.Job, error) {
	jobs, err := s.List()
	if err != nil {
		return nil, err
	}
	active := make([]supervisor.Job, 0)
	for _, job := range jobs {
		if !job.DryRun && job.State.IsUnresolvedPowerState() {
			active = append(active, job)
		}
	}
	return active, nil
}

func (s *Store) RecoverPreActionJobs() error {
	jobs, err := s.List()
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.DryRun || !job.State.IsPreActionActive() {
			continue
		}
		old := job.State
		if err := supervisor.Transition(old, supervisor.StateOrphaned); err != nil {
			return err
		}
		job.State = supervisor.StateOrphaned
		job.ReasonCode = "supervisor_recovery_orphaned"
		job.UpdatedAt = s.now().UTC()
		if err := s.Save(job); err != nil {
			return err
		}
		_ = s.AppendEvent(supervisor.Event{
			Timestamp: s.now().UTC(),
			JobID:     job.JobID,
			OldState:  old,
			NewState:  job.State,
			Reason:    job.ReasonCode,
		})
	}
	return nil
}

func (s *Store) jobsDir() string {
	return filepath.Join(s.root, "jobs")
}

func (s *Store) logsDir() string {
	return filepath.Join(s.root, "logs")
}

func (s *Store) tmpDir() string {
	return filepath.Join(s.root, "tmp")
}

func (s *Store) jobPath(jobID string) string {
	return filepath.Join(s.jobsDir(), jobID+".json")
}

func (s *Store) cancelPath(jobID string) string {
	return filepath.Join(s.jobsDir(), jobID+".cancel")
}

func (s *Store) logPath(jobID string) string {
	return filepath.Join(s.logsDir(), jobID+".jsonl")
}

func validateJobID(jobID string) error {
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

func validateJob(job supervisor.Job) error {
	if job.SchemaVersion != "2" {
		return fmt.Errorf("unsupported job schema %q", job.SchemaVersion)
	}
	switch job.State {
	case supervisor.StateArmed,
		supervisor.StateAgentRunning,
		supervisor.StateEvaluating,
		supervisor.StateVerifying,
		supervisor.StateActionIntentRecorded,
		supervisor.StateActionScheduled,
		supervisor.StateDryRunComplete,
		supervisor.StateNotDone,
		supervisor.StateAgentFailed,
		supervisor.StateInvalidCompletion,
		supervisor.StateVerificationFailed,
		supervisor.StateActionFailed,
		supervisor.StateActionExecutionUnverified,
		supervisor.StateActionExecutedConfirmed,
		supervisor.StateCancelled,
		supervisor.StateOrphaned:
	default:
		return fmt.Errorf("unknown job state %q", job.State)
	}
	if job.Action != "shutdown" {
		return fmt.Errorf("unsupported job action %q", job.Action)
	}
	if job.DelaySeconds < 30 || job.DelaySeconds > 3600 {
		return fmt.Errorf("invalid shutdown delay %d", job.DelaySeconds)
	}
	if job.DryRun && job.State.IsUnresolvedPowerState() {
		return errors.New("dry-run job cannot contain an unresolved power action")
	}
	if job.State.IsUnresolvedPowerState() && job.ActionIntentAt == nil {
		return errors.New("power-action job is missing action_intent_at")
	}
	if job.State.IsUnresolvedPowerState() && job.PowerCapabilities == nil {
		return errors.New("power-action job is missing platform capabilities")
	}
	if job.State == supervisor.StateActionScheduled && job.ScheduledFor == nil {
		return errors.New("scheduled job is missing scheduled_for")
	}
	if job.PowerReceipt != nil {
		if err := actions.ValidateReceipt(*job.PowerReceipt); err != nil {
			return fmt.Errorf("job has an invalid power receipt: %w", err)
		}
		if job.PowerReceipt.JobID != job.JobID || job.PowerReceipt.Action != job.Action {
			return errors.New("job receipt does not match the job")
		}
	}
	if job.State == supervisor.StateActionScheduled {
		if job.PowerReceipt == nil {
			return errors.New("scheduled job is missing power_receipt")
		}
		if !job.ScheduledFor.Equal(job.PowerReceipt.Deadline) {
			return errors.New("scheduled job deadline does not match its receipt")
		}
	}
	if (job.State == supervisor.StateActionExecutionUnverified || job.State == supervisor.StateActionExecutedConfirmed) &&
		(job.PowerReceipt == nil || job.ReconcileResult == nil) {
		return errors.New("reconciled job is missing receipt or reconcile result")
	}
	if job.CreatedAt.IsZero() || job.UpdatedAt.IsZero() {
		return errors.New("job timestamps are required")
	}
	return nil
}

func migrateJob(job *supervisor.Job) error {
	switch job.SchemaVersion {
	case "2":
		return nil
	case "1":
		job.SchemaVersion = "2"
	default:
		return fmt.Errorf("unsupported job schema %q", job.SchemaVersion)
	}
	if job.DryRun || (job.State != supervisor.StateActionIntentRecorded && job.State != supervisor.StateActionScheduled) {
		return nil
	}
	job.PowerCapabilities = &actions.Capabilities{
		Platform:           "windows",
		BackendID:          "windows-shutdown-exe",
		ExecuteSupported:   true,
		CancelScope:        actions.CancelScopeSystemGlobal,
		MinimumDelay:       30 * time.Second,
		MaximumDelay:       time.Hour,
		ReconcileSupported: true,
		Reason:             "migrated from legacy schema 1",
	}
	if job.State != supervisor.StateActionScheduled || job.ScheduledFor == nil {
		return nil
	}
	requestedAt := job.CreatedAt.UTC()
	if job.ActionIntentAt != nil {
		requestedAt = job.ActionIntentAt.UTC()
	}
	scheduledAt := job.UpdatedAt.UTC()
	receipt := actions.SealReceipt(actions.Receipt{
		SchemaVersion:  actions.ReceiptSchemaVersion,
		Platform:       "windows",
		BackendID:      "windows-shutdown-exe",
		BackendVersion: "legacy-schema-1",
		JobID:          job.JobID,
		Action:         job.Action,
		RequestedAt:    requestedAt,
		ScheduledAt:    scheduledAt,
		Deadline:       job.ScheduledFor.UTC(),
		CancelScope:    actions.CancelScopeSystemGlobal,
		ResultCode:     0,
		ResultSummary:  "migrated legacy Windows shutdown receipt",
	})
	job.PowerReceipt = &receipt
	return nil
}
