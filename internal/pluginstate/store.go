package pluginstate

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/filetrust"
	"github.com/andyandymike/done-then/internal/identity"
	basestore "github.com/andyandymike/done-then/internal/store"
)

const (
	maxRecordBytes        = 1 << 20
	maxProcessedEventKeys = MaximumLegacyEventKeys
	stateLockTimeout      = 500 * time.Millisecond
)

var ErrLockTimeout = errors.New("plugin state lock timed out")
var ErrTargetReservationConflict = errors.New("target session already belongs to an active DoneThen job")

type Store struct {
	root string
	now  func() time.Time
}

type Event struct {
	SchemaVersion string    `json:"schema_version"`
	Timestamp     time.Time `json:"timestamp"`
	JobID         string    `json:"job_id"`
	Name          string    `json:"name"`
	EventKey      string    `json:"event_key,omitempty"`
	OldState      State     `json:"old_state,omitempty"`
	NewState      State     `json:"new_state"`
	ReasonCode    string    `json:"reason_code,omitempty"`
	Generation    uint64    `json:"generation"`
	SessionHash   string    `json:"session_hash,omitempty"`
	TurnHash      string    `json:"turn_hash,omitempty"`
}

type sessionIndex struct {
	SchemaVersion     string `json:"schema_version"`
	JobID             string `json:"job_id"`
	TargetBindingID   string `json:"target_binding_id,omitempty"`
	TargetSessionHash string `json:"target_session_hash,omitempty"`
}

type RevocationMarker struct {
	SchemaVersion   string    `json:"schema_version"`
	JobID           string    `json:"job_id"`
	TargetBindingID string    `json:"target_binding_id"`
	TargetHash      string    `json:"target_hash"`
	TurnHash        string    `json:"turn_hash,omitempty"`
	EventKey        string    `json:"event_key"`
	Reason          string    `json:"reason"`
	Timestamp       time.Time `json:"timestamp"`
}

type BarrierAuthorityError struct {
	Reason string
}

func (e *BarrierAuthorityError) Error() string {
	return e.Reason
}

type BarrierTargetStatus struct {
	Index          int    `json:"index"`
	SessionRef     string `json:"session_ref"`
	State          string `json:"state"`
	TurnBound      bool   `json:"turn_bound"`
	WorkspaceBound bool   `json:"workspace_bound"`
}

type BarrierStatus struct {
	TargetsTotal          int                   `json:"targets_total"`
	TargetsStopped        int                   `json:"targets_stopped"`
	TargetsPending        int                   `json:"targets_pending"`
	TargetsUnseen         int                   `json:"targets_unseen"`
	ReservationsCommitted bool                  `json:"reservations_committed"`
	IndexesReady          bool                  `json:"indexes_ready"`
	Targets               []BarrierTargetStatus `json:"targets"`
}

type Status struct {
	JobID             string         `json:"job_id"`
	State             State          `json:"state"`
	ReasonCode        string         `json:"reason_code,omitempty"`
	Mode              string         `json:"mode"`
	Action            string         `json:"action"`
	TriggerPolicy     string         `json:"trigger_policy"`
	DelaySeconds      int64          `json:"delay_seconds"`
	ExpiresAt         string         `json:"expires_at"`
	Generation        uint64         `json:"generation"`
	SessionBound      bool           `json:"session_bound"`
	CompletionStatus  string         `json:"completion_status,omitempty"`
	HookCompatibility string         `json:"hook_compatibility"`
	ExecuteAvailable  bool           `json:"execute_available"`
	CancelCommand     string         `json:"cancel_command"`
	ReconcileCommand  string         `json:"reconcile_command"`
	HostSnapshots     string         `json:"host_snapshots"`
	VerifierStatus    string         `json:"verifier_status"`
	ScheduledFor      string         `json:"scheduled_for,omitempty"`
	PowerBackend      string         `json:"power_backend,omitempty"`
	CancelScope       string         `json:"cancel_scope,omitempty"`
	CancelRequested   bool           `json:"cancel_requested"`
	CancelReason      string         `json:"cancel_reason,omitempty"`
	Barrier           *BarrierStatus `json:"barrier,omitempty"`
}

func New(dataRoot string) (*Store, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, errors.New("plugin state root is empty")
	}
	absolute, err := filepath.Abs(dataRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin state root: %w", err)
	}
	state := &Store{
		root: filepath.Join(absolute, "plugin"),
		now:  time.Now,
	}
	directories := []struct {
		path  string
		label string
	}{
		{absolute, "DoneThen data root"},
		{state.root, "plugin state root"},
		{state.jobsDir(), "plugin job directory"},
		{state.sessionsDir(), "plugin session directory"},
		{state.logsDir(), "plugin event directory"},
		{state.revocationsDir(), "plugin revocation directory"},
		{state.recoveryDir(), "plugin recovery directory"},
	}
	for _, directory := range directories {
		if err := filetrust.EnsureOwnerControlledDirectory(directory.path, directory.label); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) Create(job Job) error {
	if err := migrate(&job); err != nil {
		return err
	}
	if err := Validate(job); err != nil {
		return err
	}
	lock, err := acquireStateLock(s.root, stateLockTimeout)
	if err != nil {
		return err
	}
	defer lock.Release()
	path := s.jobPath(job.JobID)
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("plugin job %s already exists", job.JobID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect plugin job: %w", err)
	}
	if err := basestore.AtomicWriteJSON(path, job); err != nil {
		return err
	}
	return s.appendEventUnlocked(job, "mcp.arm", "", "", "", StateArmPendingBind)
}

func (s *Store) CreateBarrierReservations(job Job, targetSessionIDs []string) (Job, error) {
	if err := migrate(&job); err != nil {
		return Job{}, err
	}
	if job.TriggerPolicy != TriggerAfterAllStop || job.State != StateArmPendingBind {
		return Job{}, errors.New("barrier reservations require a pending after-all-stop job")
	}
	if job.TargetReservationsCommitted || job.TargetIndexesReady {
		return Job{}, errors.New("new barrier job already claims committed reservations")
	}
	if len(targetSessionIDs) != len(job.StopTargets) {
		return Job{}, errors.New("barrier target identities do not match persisted targets")
	}
	for index, sessionID := range targetSessionIDs {
		if err := validateTargetSessionID(sessionID); err != nil {
			return Job{}, err
		}
		if identity.SHA256([]byte(sessionID)) != job.StopTargets[index].SessionHash {
			return Job{}, errors.New("barrier target hash does not match its session identity")
		}
	}
	if err := Validate(job); err != nil {
		return Job{}, err
	}
	lock, err := acquireStateLock(s.root, stateLockTimeout)
	if err != nil {
		return Job{}, err
	}
	defer lock.Release()
	if _, err := os.Lstat(s.jobPath(job.JobID)); err == nil {
		return Job{}, fmt.Errorf("plugin job %s already exists", job.JobID)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Job{}, fmt.Errorf("inspect plugin job: %w", err)
	}
	for _, target := range job.StopTargets {
		index, found, readErr := s.readSessionIndexHashUnlocked(target.SessionHash)
		if readErr != nil {
			return Job{}, readErr
		}
		if !found {
			continue
		}
		existing, loadErr := s.loadPath(s.jobPath(index.JobID), index.JobID)
		if loadErr != nil {
			return Job{}, fmt.Errorf("inspect existing target reservation: %w", loadErr)
		}
		if existing.TriggerPolicy == TriggerAfterAllStop && !existing.TargetReservationsCommitted &&
			index.TargetBindingID == existing.TargetBindingID {
			if err := s.removeMatchingSessionIndexUnlocked(target.SessionHash, existing.JobID, existing.TargetBindingID); err != nil {
				return Job{}, err
			}
			continue
		}
		if existing.State.IsActive() || HasUnresolvedPowerAction(existing) {
			return Job{}, fmt.Errorf("%w: %s", ErrTargetReservationConflict, existing.JobID)
		}
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(job.JobID), job); err != nil {
		return Job{}, err
	}
	written := make([]string, 0, len(job.StopTargets))
	for _, target := range job.StopTargets {
		index := sessionIndex{
			SchemaVersion:     "2",
			JobID:             job.JobID,
			TargetBindingID:   job.TargetBindingID,
			TargetSessionHash: target.SessionHash,
		}
		if err := s.writeSessionIndexHashUnlocked(target.SessionHash, index); err != nil {
			s.failBarrierReservationUnlocked(&job, written)
			return Job{}, fmt.Errorf("write barrier target reservation: %w", err)
		}
		written = append(written, target.SessionHash)
	}
	job.TargetReservationsCommitted = true
	job.UpdatedAt = s.now().UTC()
	if err := Validate(job); err != nil {
		s.failBarrierReservationUnlocked(&job, written)
		return Job{}, err
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(job.JobID), job); err != nil {
		s.failBarrierReservationUnlocked(&job, written)
		return Job{}, fmt.Errorf("commit barrier target reservations: %w", err)
	}
	if err := s.appendEventUnlocked(job, "mcp.arm.barrier_reserved", "", "", "", StateArmPendingBind); err != nil {
		job.Generation++
		job.State = StateHookUnavailable
		job.ReasonCode = "barrier_reservation_partial"
		job.UpdatedAt = s.now().UTC()
		_ = basestore.AtomicWriteJSON(s.jobPath(job.JobID), job)
		_ = s.releaseBarrierIndexesUnlocked(job)
		return Job{}, err
	}
	return job, nil
}

func (s *Store) failBarrierReservationUnlocked(job *Job, written []string) {
	job.Generation++
	job.State = StateHookUnavailable
	job.ReasonCode = "barrier_reservation_partial"
	job.UpdatedAt = s.now().UTC()
	_ = basestore.AtomicWriteJSON(s.jobPath(job.JobID), *job)
	for _, sessionHash := range written {
		_ = s.removeMatchingSessionIndexUnlocked(sessionHash, job.JobID, job.TargetBindingID)
	}
}

func (s *Store) Load(jobID string) (Job, error) {
	if err := ValidateJobID(jobID); err != nil {
		return Job{}, err
	}
	return s.loadPath(s.jobPath(jobID), jobID)
}

func (s *Store) List() ([]Job, error) {
	entries, err := os.ReadDir(s.jobsDir())
	if err != nil {
		return nil, fmt.Errorf("list plugin jobs: %w", err)
	}
	jobs := make([]Job, 0, len(entries))
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

func (s *Store) UpdateJob(jobID, eventName, eventKey string, mutate func(*Job, time.Time) error) (Job, bool, error) {
	if err := ValidateJobID(jobID); err != nil {
		return Job{}, false, err
	}
	if err := validateEventKey(eventKey); err != nil {
		return Job{}, false, err
	}
	lock, err := acquireStateLock(s.root, stateLockTimeout)
	if err != nil {
		return Job{}, false, err
	}
	defer lock.Release()
	job, err := s.loadPath(s.jobPath(jobID), jobID)
	if err != nil {
		return Job{}, false, err
	}
	if eventKey != "" && hasEventKey(job, eventKey) {
		return job, false, nil
	}
	oldState := job.State
	now := s.now().UTC()
	if err := mutate(&job, now); err != nil {
		return job, false, err
	}
	if eventKey != "" {
		if err := appendProcessedEventKey(&job, eventKey); err != nil {
			return Job{}, false, err
		}
	}
	job.UpdatedAt = now
	if err := Validate(job); err != nil {
		return Job{}, false, fmt.Errorf("validate updated plugin job: %w", err)
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(jobID), job); err != nil {
		return Job{}, false, err
	}
	if shouldReleaseBarrierIndexes(job) {
		if err := s.releaseBarrierIndexesUnlocked(job); err != nil {
			return Job{}, false, err
		}
	}
	if err := s.appendEventUnlocked(job, eventName, eventKey, "", "", oldState); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (s *Store) BindSession(jobID, sessionID, turnID, workspace, eventKey string) (Job, bool, error) {
	if err := validateBinding(sessionID, turnID); err != nil {
		return Job{}, false, err
	}
	if err := ValidateJobID(jobID); err != nil {
		return Job{}, false, err
	}
	if err := validateEventKey(eventKey); err != nil {
		return Job{}, false, err
	}
	lock, err := acquireStateLock(s.root, stateLockTimeout)
	if err != nil {
		return Job{}, false, err
	}
	defer lock.Release()
	job, err := s.loadPath(s.jobPath(jobID), jobID)
	if err != nil {
		return Job{}, false, err
	}
	if job.TriggerPolicy == TriggerAfterAllStop {
		return s.bindBarrierControllerUnlocked(job, sessionID, turnID, workspace, eventKey)
	}
	if eventKey != "" && hasEventKey(job, eventKey) {
		if job.SessionID == sessionID {
			if err := s.writeSessionIndexUnlocked(sessionID, job.JobID); err != nil {
				return Job{}, false, err
			}
		}
		return job, false, nil
	}
	now := s.now().UTC()
	oldState := job.State
	if expire(&job, now) {
		// Persist expiry below. A late hook must never revive the job.
	} else if job.State == StateArmPendingBind {
		if job.TriggerPolicy == TriggerAfterStop && !job.DryRun {
			if !filepath.IsAbs(workspace) {
				return Job{}, false, errors.New("after-stop execute hook is missing an absolute workspace")
			}
			job.WorkspaceCWD = filepath.Clean(workspace)
		} else if job.TriggerPolicy == TriggerVerifiedSuccess && !WorkspaceMatches(job.WorkspaceCWD, workspace) {
			return Job{}, false, errors.New("arm hook workspace does not match the MCP workspace")
		}
		if existing, found, err := s.findBySessionUnlocked(sessionID); err != nil {
			return Job{}, false, err
		} else if found && existing.JobID != job.JobID && existing.State.IsActive() {
			return Job{}, false, fmt.Errorf("session already has active plugin job %s", existing.JobID)
		}
		job.SessionID = sessionID
		job.ArmTurnID = turnID
		job.CurrentTurnID = turnID
		job.ArmObserved = true
		job.State = StateArmed
		job.ReasonCode = "hook_bound"
		if job.TriggerPolicy == TriggerAfterStop {
			job.HookCompatibility = "session_bound"
		}
	} else if job.SessionID != sessionID || job.ArmTurnID != turnID {
		return Job{}, false, errors.New("arm hook binding does not match the existing job binding")
	}
	if eventKey != "" {
		job.ProcessedEventKeys = appendEventKey(job.ProcessedEventKeys, eventKey)
	}
	job.UpdatedAt = now
	if err := Validate(job); err != nil {
		return Job{}, false, fmt.Errorf("validate bound plugin job: %w", err)
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(jobID), job); err != nil {
		return Job{}, false, err
	}
	if job.SessionID != "" {
		if err := s.writeSessionIndexUnlocked(sessionID, job.JobID); err != nil {
			return Job{}, false, err
		}
	}
	if err := s.appendEventUnlocked(job, "hook.post_tool.arm", eventKey, identity.SHA256([]byte(sessionID)), identity.SHA256([]byte(turnID)), oldState); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (s *Store) bindBarrierControllerUnlocked(job Job, sessionID, turnID, workspace, eventKey string) (Job, bool, error) {
	controllerHash := identity.SHA256([]byte(sessionID))
	turnHash := identity.SHA256([]byte(turnID))
	if eventKey != "" && hasEventKey(job, eventKey) {
		return job, false, nil
	}
	now := s.now().UTC()
	oldState := job.State
	if expire(&job, now) {
		// Persist expiry below. A late controller hook cannot revive the job.
	} else if job.State == StateArmPendingBind {
		if !job.TargetReservationsCommitted || !filepath.IsAbs(workspace) || !s.barrierIndexesMatchUnlocked(job) {
			job.Generation++
			job.State = StateHookUnavailable
			job.ReasonCode = "barrier_binding_incomplete"
			job.TargetIndexesReady = false
		} else {
			job.ControllerSessionHash = controllerHash
			job.ControllerArmTurnHash = turnHash
			job.WorkspaceCWD = filepath.Clean(workspace)
			job.ArmObserved = true
			job.TargetIndexesReady = true
			job.HookCompatibility = "multi_session_bound"
			job.Generation++
			if job.BarrierSatisfied() {
				if job.DryRun {
					job.State = StateDryRunComplete
					job.ReasonCode = "after_all_stop_observed_no_action"
				} else {
					job.State = StateStopObserved
					job.ReasonCode = "after_all_stop_observed_awaiting_countdown"
				}
			} else {
				job.State = StateArmed
				job.ReasonCode = "after_all_stop_barrier_partial"
			}
		}
	} else if job.ControllerSessionHash != controllerHash || job.ControllerArmTurnHash != turnHash {
		return Job{}, false, errors.New("arm hook controller binding does not match the existing barrier binding")
	}
	if eventKey != "" {
		if err := appendProcessedEventKey(&job, eventKey); err != nil {
			return Job{}, false, err
		}
	}
	job.UpdatedAt = now
	if err := Validate(job); err != nil {
		return Job{}, false, fmt.Errorf("validate bound barrier job: %w", err)
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(job.JobID), job); err != nil {
		return Job{}, false, err
	}
	if shouldReleaseBarrierIndexes(job) {
		if err := s.releaseBarrierIndexesUnlocked(job); err != nil {
			return Job{}, false, err
		}
	}
	if err := s.appendEventUnlocked(job, "hook.post_tool.arm", eventKey, controllerHash, turnHash, oldState); err != nil {
		s.failBarrierObserverWriteUnlocked(&job, "barrier_event_log_unavailable")
		return Job{}, false, err
	}
	return job, true, nil
}

func WorkspaceMatches(expected, observed string) bool {
	if expected == "" {
		return true
	}
	if !filepath.IsAbs(expected) || !filepath.IsAbs(observed) {
		return false
	}
	expected = filepath.Clean(expected)
	observed = filepath.Clean(observed)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(expected, observed)
	}
	return expected == observed
}

func (s *Store) writeSessionIndexUnlocked(sessionID, jobID string) error {
	index := sessionIndex{SchemaVersion: "1", JobID: jobID}
	return basestore.AtomicWriteJSON(s.sessionPath(sessionID), index)
}

func (s *Store) writeSessionIndexHashUnlocked(sessionHash string, index sessionIndex) error {
	if !ValidIdentityHash(sessionHash) || index.TargetSessionHash != sessionHash || index.SchemaVersion != "2" {
		return errors.New("invalid barrier session index")
	}
	return basestore.AtomicWriteJSON(s.sessionHashPath(sessionHash), index)
}

func (s *Store) readSessionIndexHashUnlocked(sessionHash string) (sessionIndex, bool, error) {
	if !ValidIdentityHash(sessionHash) {
		return sessionIndex{}, false, errors.New("invalid session hash")
	}
	file, info, err := filetrust.OpenOwnerControlled(s.sessionHashPath(sessionHash), "plugin session index")
	if errors.Is(err, os.ErrNotExist) {
		return sessionIndex{}, false, nil
	}
	if err != nil {
		return sessionIndex{}, false, err
	}
	defer file.Close()
	if info.Size() > 4096 {
		return sessionIndex{}, false, errors.New("plugin session index exceeds 4096 bytes")
	}
	var index sessionIndex
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return sessionIndex{}, false, fmt.Errorf("decode plugin session index: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return sessionIndex{}, false, errors.New("plugin session index contains trailing data")
	}
	if index.SchemaVersion != "1" && index.SchemaVersion != "2" {
		return sessionIndex{}, false, errors.New("unsupported plugin session index")
	}
	if err := ValidateJobID(index.JobID); err != nil {
		return sessionIndex{}, false, errors.New("plugin session index has an invalid job id")
	}
	if index.SchemaVersion == "1" {
		if index.TargetBindingID != "" || index.TargetSessionHash != "" {
			return sessionIndex{}, false, errors.New("legacy plugin session index carries barrier fields")
		}
	} else if index.TargetSessionHash != sessionHash || !ValidIdentityHash(index.TargetSessionHash) {
		return sessionIndex{}, false, errors.New("barrier session index hash mismatch")
	} else if err := ValidateJobID(index.TargetBindingID); err != nil {
		return sessionIndex{}, false, errors.New("barrier session index has an invalid binding id")
	}
	return index, true, nil

}

func (s *Store) barrierIndexesMatchUnlocked(job Job) bool {
	if job.TriggerPolicy != TriggerAfterAllStop || !job.TargetReservationsCommitted {
		return false
	}
	for _, target := range job.StopTargets {
		index, found, err := s.readSessionIndexHashUnlocked(target.SessionHash)
		if err != nil || !found || index.SchemaVersion != "2" || index.JobID != job.JobID ||
			index.TargetBindingID != job.TargetBindingID || index.TargetSessionHash != target.SessionHash {
			return false
		}
	}
	return true

}

func (s *Store) removeMatchingSessionIndexUnlocked(sessionHash, jobID, bindingID string) error {
	index, found, err := s.readSessionIndexHashUnlocked(sessionHash)
	if err != nil || !found {
		return err
	}
	if index.JobID != jobID || index.TargetBindingID != bindingID || index.TargetSessionHash != sessionHash {
		return nil
	}
	if err := os.Remove(s.sessionHashPath(sessionHash)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove barrier session index: %w", err)
	}
	return nil

}

func (s *Store) releaseBarrierIndexesUnlocked(job Job) error {
	for _, target := range job.StopTargets {
		if err := s.removeMatchingSessionIndexUnlocked(target.SessionHash, job.JobID, job.TargetBindingID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpdateSession(sessionID, turnID, eventName, eventKey string, mutate func(*Job, time.Time) error) (Job, bool, bool, error) {
	return s.UpdateObservedSession(sessionID, turnID, eventName, eventKey, func(job *Job, _ *StopTarget, now time.Time) error {
		return mutate(job, now)
	})
}

func (s *Store) UpdateObservedSession(sessionID, turnID, eventName, eventKey string, mutate func(*Job, *StopTarget, time.Time) error) (Job, bool, bool, error) {
	return s.updateObservedSessionBinding(sessionID, turnID, eventName, eventKey, "", "", false, mutate)
}

func (s *Store) UpdateObservedSessionBinding(sessionID, turnID, eventName, eventKey, expectedJobID, expectedBindingID string, mutate func(*Job, *StopTarget, time.Time) error) (Job, bool, bool, error) {
	return s.updateObservedSessionBinding(sessionID, turnID, eventName, eventKey, expectedJobID, expectedBindingID, true, mutate)
}

func (s *Store) updateObservedSessionBinding(sessionID, turnID, eventName, eventKey, expectedJobID, expectedBindingID string, requireCapturedBinding bool, mutate func(*Job, *StopTarget, time.Time) error) (Job, bool, bool, error) {
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 1024 {
		return Job{}, false, false, errors.New("invalid hook session id")
	}
	if len(turnID) > 1024 {
		return Job{}, false, false, errors.New("invalid hook turn id")
	}
	if err := validateEventKey(eventKey); err != nil {
		return Job{}, false, false, err
	}
	lock, err := acquireStateLock(s.root, stateLockTimeout)
	if err != nil {
		return Job{}, false, false, err
	}
	defer lock.Release()
	sessionHash := identity.SHA256([]byte(sessionID))
	job, found, err := s.findBySessionUnlocked(sessionID)
	if err != nil || !found {
		return Job{}, false, found, err
	}
	if requireCapturedBinding && (expectedJobID == "" || expectedBindingID == "" ||
		job.JobID != expectedJobID || ObservedBindingID(job) != expectedBindingID) {
		return job, false, true, nil
	}
	// Target indexes are reservations while the controller arm PostToolUse is
	// pending. They deliberately do not make target lifecycle events causal:
	// an event that races the MCP response cannot be credited to the barrier.
	if job.TriggerPolicy == TriggerAfterAllStop && (!job.TargetReservationsCommitted || !job.TargetIndexesReady || !job.ArmObserved) {
		return job, false, true, nil
	}
	if eventKey != "" && hasEventKey(job, eventKey) {
		return job, false, true, nil
	}
	oldState := job.State
	now := s.now().UTC()
	var target *StopTarget
	if job.TriggerPolicy == TriggerAfterAllStop {
		var targetFound bool
		target, targetFound = job.StopTarget(sessionHash)
		if !targetFound {
			return Job{}, false, true, errors.New("barrier session index does not name a target")
		}
		if eventKey != "" && len(job.ProcessedEventKeys) >= MaximumBarrierEventKeys {
			job.Generation++
			if HasUnresolvedPowerAction(job) {
				job.CancelRequested = true
				job.CancelReason = "barrier_event_capacity_exhausted"
				job.ReasonCode = "barrier_event_capacity_exhausted"
			} else {
				job.State = StateHookUnavailable
				job.ReasonCode = "barrier_event_capacity_exhausted"
			}
			job.UpdatedAt = now
			if err := Validate(job); err != nil {
				return Job{}, false, true, err
			}
			if err := basestore.AtomicWriteJSON(s.jobPath(job.JobID), job); err != nil {
				return Job{}, false, true, err
			}
			if shouldReleaseBarrierIndexes(job) {
				_ = s.releaseBarrierIndexesUnlocked(job)
			}
			_ = s.appendEventUnlocked(job, eventName, "", sessionHash, hashOptional(turnID), oldState)
			return job, true, true, nil
		}
	}
	if err := mutate(&job, target, now); err != nil {
		return job, false, true, err
	}
	if eventKey != "" {
		if err := appendProcessedEventKey(&job, eventKey); err != nil {
			return Job{}, false, true, err
		}
	}
	job.UpdatedAt = now
	if err := Validate(job); err != nil {
		return Job{}, false, true, fmt.Errorf("validate observed plugin job: %w", err)
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(job.JobID), job); err != nil {
		return Job{}, false, true, err
	}
	if shouldReleaseBarrierIndexes(job) {
		if err := s.releaseBarrierIndexesUnlocked(job); err != nil {
			return Job{}, false, true, err
		}
	}
	if err := s.appendEventUnlocked(job, eventName, eventKey, sessionHash, hashOptional(turnID), oldState); err != nil {
		if job.TriggerPolicy == TriggerAfterAllStop {
			s.failBarrierObserverWriteUnlocked(&job, "barrier_event_log_unavailable")
		}
		return Job{}, false, true, err
	}
	return job, true, true, nil
}

// ObservedBindingID returns the immutable binding identity used to prevent an
// event captured before arm from being applied to a concurrently-created job.
// Legacy single-session jobs use their immutable JobID; barriers use the
// independently generated target binding identity.
func ObservedBindingID(job Job) string {
	if job.TriggerPolicy == TriggerAfterAllStop {
		return job.TargetBindingID
	}
	return job.JobID
}

func (s *Store) LookupObservedSession(sessionID string) (Job, StopTarget, bool, error) {
	if err := validateTargetSessionID(sessionID); err != nil {
		return Job{}, StopTarget{}, false, err
	}
	// Session indexes and job files are atomically replaced. A read-only
	// capture does not take the global writer lock; the subsequent mutation is
	// serialized and must match the captured immutable binding exactly.
	job, found, err := s.findBySessionUnlocked(sessionID)
	if err != nil || !found {
		return Job{}, StopTarget{}, found, err
	}
	if job.TriggerPolicy != TriggerAfterAllStop {
		return job, StopTarget{}, true, nil
	}
	if !job.TargetReservationsCommitted || !job.TargetIndexesReady || !job.ArmObserved {
		// Reservation indexes prevent overlap, but do not expose an observable
		// target binding until the controller arm PostToolUse commits.
		return Job{}, StopTarget{}, false, nil
	}
	target, found := job.StopTarget(identity.SHA256([]byte(sessionID)))
	if !found {
		return Job{}, StopTarget{}, false, errors.New("barrier target reservation is inconsistent")
	}
	return job, *target, true, nil
}

func (s *Store) LookupTargetSession(sessionID string) (Job, StopTarget, bool, error) {
	job, target, found, err := s.LookupObservedSession(sessionID)
	if err != nil || !found {
		return Job{}, StopTarget{}, found, err
	}
	if job.TriggerPolicy != TriggerAfterAllStop || !job.TargetReservationsCommitted {
		return Job{}, StopTarget{}, false, nil
	}
	return job, target, true, nil
}

func (s *Store) CreateRevocationMarker(sessionID, turnID, eventKey, reason, expectedJobID, expectedBindingID string) (RevocationMarker, bool, error) {
	if err := validateEventKey(eventKey); err != nil || eventKey == "" {
		if err == nil {
			err = errors.New("revocation marker requires an event key")
		}
		return RevocationMarker{}, false, err
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 256 {
		return RevocationMarker{}, false, errors.New("invalid revocation reason")
	}
	job, _, found, lookupErr := s.LookupObservedSession(sessionID)
	if found && expectedJobID != "" && (job.JobID != expectedJobID || ObservedBindingID(job) != expectedBindingID) {
		return RevocationMarker{}, false, nil
	}
	if !found && expectedJobID == "" {
		return RevocationMarker{}, false, lookupErr
	}
	if found && job.State.IsTerminal() && !HasUnresolvedPowerAction(job) {
		return RevocationMarker{}, false, lookupErr
	}
	jobID := expectedJobID
	bindingID := expectedBindingID
	if found {
		jobID = job.JobID
		bindingID = ObservedBindingID(job)
	}
	if ValidateJobID(jobID) != nil || ValidateJobID(bindingID) != nil {
		return RevocationMarker{}, expectedJobID != "", errors.Join(lookupErr, errors.New("revocation binding is unavailable"))
	}
	marker := RevocationMarker{
		SchemaVersion: "1", JobID: jobID, TargetBindingID: bindingID,
		TargetHash: identity.SHA256([]byte(sessionID)), TurnHash: hashOptional(turnID), EventKey: eventKey,
		Reason: reason, Timestamp: s.now().UTC(),
	}
	if err := s.writeRevocationCreateOnce(marker); err != nil {
		return marker, true, errors.Join(lookupErr, err)
	}
	return marker, true, lookupErr
}

func (s *Store) CompleteRevocation(marker RevocationMarker) error {
	path, err := s.revocationPath(marker)
	if err != nil {
		return err
	}
	current, err := s.loadRevocation(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if current.JobID != marker.JobID || current.TargetBindingID != marker.TargetBindingID ||
		current.TargetHash != marker.TargetHash || current.EventKey != marker.EventKey {
		return errors.New("revocation marker changed before cleanup")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove processed revocation marker: %w", err)
	}
	return nil
}

func (s *Store) CompletePendingRevocations(jobID, bindingID string) error {
	if ValidateJobID(jobID) != nil || ValidateJobID(bindingID) != nil {
		return errors.New("invalid revocation binding")
	}
	directory := filepath.Join(s.revocationsDir(), jobID, bindingID)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read revocation directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return errors.New("revocation directory contains an unexpected entry")
		}
		marker, loadErr := s.loadRevocation(filepath.Join(directory, entry.Name()))
		if loadErr != nil {
			return loadErr
		}
		if marker.JobID != jobID || marker.TargetBindingID != bindingID {
			return errors.New("revocation marker binding mismatch")
		}
		if err := s.CompleteRevocation(marker); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) PendingRevocation(jobID string) (RevocationMarker, bool, error) {
	lock, err := acquireStateLock(s.root, stateLockTimeout)
	if err != nil {
		return RevocationMarker{}, false, err
	}
	defer lock.Release()
	job, err := s.loadPath(s.jobPath(jobID), jobID)
	if err != nil {
		return RevocationMarker{}, false, err
	}
	pending, err := s.pendingRevocationsUnlocked(job)
	if err != nil || len(pending) == 0 {
		return RevocationMarker{}, false, err
	}
	return pending[0], true, nil
}

func (s *Store) BarrierAuthority(jobID string) (Job, error) {
	lock, err := acquireStateLock(s.root, stateLockTimeout)
	if err != nil {
		return Job{}, err
	}
	defer lock.Release()
	job, err := s.loadPath(s.jobPath(jobID), jobID)
	if err != nil {
		return Job{}, err
	}
	if job.TriggerPolicy != TriggerAfterAllStop || !job.StopWithoutSuccessAck ||
		job.ControllerSessionHash == "" || job.ControllerArmTurnHash == "" || !job.ArmObserved ||
		!job.TargetReservationsCommitted || !job.TargetIndexesReady {
		return job, &BarrierAuthorityError{Reason: "barrier_binding_incomplete"}
	}
	if !job.BarrierSatisfied() {
		return job, &BarrierAuthorityError{Reason: "after_all_stop_target_resumed"}
	}
	for _, target := range job.StopTargets {
		if target.WorkspaceCWD == "" || !filepath.IsAbs(target.WorkspaceCWD) {
			return job, &BarrierAuthorityError{Reason: "target_workspace_changed"}
		}
	}
	if !s.barrierIndexesMatchUnlocked(job) {
		return job, &BarrierAuthorityError{Reason: "target_index_changed"}
	}
	pending, err := s.pendingRevocationsUnlocked(job)
	if err != nil {
		return job, &BarrierAuthorityError{Reason: "revocation_channel_unavailable"}
	}
	if len(pending) != 0 {
		return job, &BarrierAuthorityError{Reason: "revocation_pending:" + pending[0].Reason}
	}
	if job.CancelRequested {
		return job, &BarrierAuthorityError{Reason: valueOr(job.CancelReason, "barrier_cancel_requested")}
	}
	return job, nil
}

func (s *Store) writeRevocationCreateOnce(marker RevocationMarker) (returnedErr error) {
	path, err := s.revocationPath(marker)
	if err != nil {
		return err
	}
	if err := filetrust.EnsureOwnerControlledDirectory(filepath.Dir(path), "barrier revocation directory"); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		existing, loadErr := s.loadRevocation(path)
		if loadErr != nil {
			return loadErr
		}
		if existing.JobID == marker.JobID && existing.TargetBindingID == marker.TargetBindingID &&
			existing.TargetHash == marker.TargetHash && existing.EventKey == marker.EventKey && existing.Reason == marker.Reason {
			return nil
		}
		return errors.New("revocation event key collision")
	}
	if err != nil {
		return fmt.Errorf("create revocation marker: %w", err)
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
	if err := json.NewEncoder(file).Encode(marker); err != nil {
		return fmt.Errorf("encode revocation marker: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush revocation marker: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close revocation marker: %w", err)
	}
	return nil
}

func (s *Store) pendingRevocationsUnlocked(job Job) ([]RevocationMarker, error) {
	bindingID := ObservedBindingID(job)
	if ValidateJobID(bindingID) != nil {
		return nil, errors.New("job revocation binding is invalid")
	}
	directory := filepath.Join(s.revocationsDir(), job.JobID, bindingID)
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read barrier revocation directory: %w", err)
	}
	pending := make([]RevocationMarker, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			return nil, errors.New("barrier revocation directory contains an unexpected entry")
		}
		marker, loadErr := s.loadRevocation(filepath.Join(directory, entry.Name()))
		if loadErr != nil {
			return nil, loadErr
		}
		if marker.JobID != job.JobID || marker.TargetBindingID != bindingID {
			return nil, errors.New("revocation marker binding mismatch")
		}
		if job.TriggerPolicy == TriggerAfterAllStop {
			if _, found := job.StopTarget(marker.TargetHash); !found {
				return nil, errors.New("barrier revocation marker target mismatch")
			}
		} else if job.SessionID == "" || marker.TargetHash != identity.SHA256([]byte(job.SessionID)) {
			return nil, errors.New("revocation marker session mismatch")
		}
		pending = append(pending, marker)
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].Timestamp.Before(pending[j].Timestamp) })
	return pending, nil
}

func (s *Store) loadRevocation(path string) (RevocationMarker, error) {
	file, info, err := filetrust.OpenOwnerControlled(path, "barrier revocation marker")
	if err != nil {
		return RevocationMarker{}, err
	}
	defer file.Close()
	if info.Size() > 8192 {
		return RevocationMarker{}, errors.New("barrier revocation marker is too large")
	}
	var marker RevocationMarker
	decoder := json.NewDecoder(io.LimitReader(file, 8193))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil {
		return RevocationMarker{}, fmt.Errorf("decode barrier revocation marker: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return RevocationMarker{}, errors.New("barrier revocation marker contains trailing data")
	}
	if marker.SchemaVersion != "1" || !ValidIdentityHash(marker.TargetHash) ||
		(marker.TurnHash != "" && !ValidIdentityHash(marker.TurnHash)) || validateEventKey(marker.EventKey) != nil ||
		ValidateJobID(marker.JobID) != nil || ValidateJobID(marker.TargetBindingID) != nil ||
		strings.TrimSpace(marker.Reason) == "" || marker.Timestamp.IsZero() {
		return RevocationMarker{}, errors.New("invalid barrier revocation marker")
	}
	return marker, nil
}

func (s *Store) revocationPath(marker RevocationMarker) (string, error) {
	if ValidateJobID(marker.JobID) != nil || ValidateJobID(marker.TargetBindingID) != nil || validateEventKey(marker.EventKey) != nil || marker.EventKey == "" {
		return "", errors.New("invalid revocation marker identity")
	}
	return filepath.Join(s.revocationsDir(), marker.JobID, marker.TargetBindingID, marker.EventKey+".json"), nil
}

func valueOr(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

func (s *Store) failBarrierObserverWriteUnlocked(job *Job, reason string) {
	job.Generation++
	if HasUnresolvedPowerAction(*job) {
		job.CancelRequested = true
		job.CancelReason = reason
		job.ReasonCode = reason
	} else {
		job.State = StateHookUnavailable
		job.ReasonCode = reason
	}
	job.UpdatedAt = s.now().UTC()
	_ = basestore.AtomicWriteJSON(s.jobPath(job.JobID), *job)
	if shouldReleaseBarrierIndexes(*job) {
		_ = s.releaseBarrierIndexesUnlocked(*job)
	}
}

func (s *Store) Status(job Job) Status {
	mode := "execute"
	if job.DryRun {
		mode = "dry_run"
	}
	hostSnapshots := "0/3"
	count := 0
	for _, fingerprint := range []string{job.HookFingerprintH1, job.HookFingerprintH2, job.HookFingerprintH3} {
		if fingerprint != "" {
			count++
		}
	}
	hostSnapshots = fmt.Sprintf("%d/3", count)
	verifierStatus := "pending"
	if job.TriggerPolicy == TriggerAfterStop || job.TriggerPolicy == TriggerAfterAllStop {
		verifierStatus = "not_required"
		hostSnapshots = "not_required"
	} else if job.VerifierProfile == "none" {
		verifierStatus = "agent-only"
	} else if job.VerifierPassed {
		verifierStatus = "passed"
	} else if job.State == StateVerificationFailed {
		verifierStatus = "failed"
	}
	scheduledFor := ""
	if job.ScheduledFor != nil {
		scheduledFor = job.ScheduledFor.UTC().Format(time.RFC3339)
	}
	powerBackend := ""
	cancelScope := ""
	if job.PowerCapabilities != nil {
		powerBackend = job.PowerCapabilities.BackendID
		cancelScope = string(job.PowerCapabilities.CancelScope)
	}
	sessionBound := job.SessionID != ""
	var barrier *BarrierStatus
	if job.TriggerPolicy == TriggerAfterAllStop {
		sessionBound = job.ControllerSessionHash != "" && job.ControllerArmTurnHash != "" && job.TargetIndexesReady
		stopped, unseen := job.BarrierProgress()
		targets := make([]BarrierTargetStatus, 0, len(job.StopTargets))
		prefixes := uniqueSessionPrefixes(job.StopTargets)
		for index, target := range job.StopTargets {
			state := "running"
			if target.FirstSeenAt == nil {
				state = "unseen"
			} else if target.Stopped() {
				state = "stopped"
			}
			targets = append(targets, BarrierTargetStatus{
				Index: index + 1, SessionRef: "sha256:" + prefixes[index], State: state,
				TurnBound: target.CurrentTurnHash != "", WorkspaceBound: target.WorkspaceCWD != "",
			})
		}
		barrier = &BarrierStatus{
			TargetsTotal: len(job.StopTargets), TargetsStopped: stopped,
			TargetsPending: len(job.StopTargets) - stopped, TargetsUnseen: unseen,
			ReservationsCommitted: job.TargetReservationsCommitted, IndexesReady: job.TargetIndexesReady,
			Targets: targets,
		}
	}
	return Status{
		JobID:             job.JobID,
		State:             job.State,
		ReasonCode:        job.ReasonCode,
		Mode:              mode,
		Action:            job.Action,
		TriggerPolicy:     string(job.TriggerPolicy),
		DelaySeconds:      job.DelaySeconds,
		ExpiresAt:         job.ExpiresAt.UTC().Format(time.RFC3339),
		Generation:        job.Generation,
		SessionBound:      sessionBound,
		CompletionStatus:  job.CompletionStatus,
		HookCompatibility: job.HookCompatibility,
		// Environment and policy readiness are runtime facts supplied by the
		// service. Persisted job mode alone must never be reported as authority.
		ExecuteAvailable: false,
		CancelCommand:    "donethen cancel " + job.JobID,
		ReconcileCommand: "donethen reconcile " + job.JobID,
		HostSnapshots:    hostSnapshots,
		VerifierStatus:   verifierStatus,
		ScheduledFor:     scheduledFor,
		PowerBackend:     powerBackend,
		CancelScope:      cancelScope,
		CancelRequested:  job.CancelRequested,
		CancelReason:     job.CancelReason,
		Barrier:          barrier,
	}
}

func uniqueSessionPrefixes(targets []StopTarget) []string {
	prefixes := make([]string, len(targets))
	for index, target := range targets {
		length := 8
		for length < len(target.SessionHash) {
			unique := true
			candidate := target.SessionHash[:length]
			for otherIndex, other := range targets {
				if otherIndex != index && strings.HasPrefix(other.SessionHash, candidate) {
					unique = false
					break
				}
			}
			if unique {
				break
			}
			length++
		}
		prefixes[index] = target.SessionHash[:length]
	}
	return prefixes
}

func (s *Store) RefreshExpiry(jobID string) (Job, error) {
	current, err := s.Load(jobID)
	if err != nil {
		return Job{}, err
	}
	if !current.State.IsActive() || !current.Expired(s.now().UTC()) {
		return current, nil
	}
	job, _, err := s.UpdateJob(jobID, "state.expiry_check", "", func(job *Job, now time.Time) error {
		expire(job, now)
		return nil
	})
	return job, err
}

func expire(job *Job, now time.Time) bool {
	if job.State.IsActive() && job.State != StateActionIntent && job.State != StateActionScheduled && job.Expired(now) {
		job.Generation++
		job.State = StateExpired
		job.ReasonCode = "arm_expired"
		job.CompletionEvidenceHash = ""
		job.ReadyTurnID = ""
		job.StopTurnID = ""
		return true
	}
	return false
}

func (s *Store) loadPath(path, expectedJobID string) (Job, error) {
	file, info, err := filetrust.OpenOwnerControlled(path, "plugin job "+expectedJobID)
	if err != nil {
		return Job{}, err
	}
	defer file.Close()
	if info.Size() > maxRecordBytes {
		return Job{}, fmt.Errorf("plugin job %s exceeds %d bytes", expectedJobID, maxRecordBytes)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxRecordBytes+1))
	decoder.DisallowUnknownFields()
	var job Job
	if err := decoder.Decode(&job); err != nil {
		return Job{}, fmt.Errorf("decode plugin job %s: %w", expectedJobID, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Job{}, fmt.Errorf("plugin job %s contains trailing data", expectedJobID)
	}
	if job.JobID != expectedJobID {
		return Job{}, fmt.Errorf("plugin job %s has an invalid identity", expectedJobID)
	}
	if err := migrate(&job); err != nil {
		return Job{}, fmt.Errorf("plugin job %s cannot be migrated: %w", expectedJobID, err)
	}
	if err := Validate(job); err != nil {
		return Job{}, fmt.Errorf("plugin job %s is invalid: %w", expectedJobID, err)
	}
	return job, nil
}

func migrate(job *Job) error {
	switch job.SchemaVersion {
	case CurrentSchemaVersion:
		return nil
	case "3":
		job.SchemaVersion = CurrentSchemaVersion
		return nil
	case "2":
		job.SchemaVersion = CurrentSchemaVersion
		job.TriggerPolicy = TriggerVerifiedSuccess
		job.StopWithoutSuccessAck = false
		return nil
	case "1":
		if !job.DryRun {
			return errors.New("legacy plugin schema unexpectedly contains an execute job")
		}
		job.SchemaVersion = CurrentSchemaVersion
		job.TriggerPolicy = TriggerVerifiedSuccess
		job.StopWithoutSuccessAck = false
		return nil
	default:
		return fmt.Errorf("unsupported plugin job schema %q", job.SchemaVersion)
	}
}

func (s *Store) findBySessionUnlocked(sessionID string) (Job, bool, error) {
	sessionHash := identity.SHA256([]byte(sessionID))
	index, found, err := s.readSessionIndexHashUnlocked(sessionHash)
	if err != nil || !found {
		return Job{}, found, err
	}
	job, err := s.loadPath(s.jobPath(index.JobID), index.JobID)
	if err != nil {
		return Job{}, false, err
	}
	if index.SchemaVersion == "1" {
		if job.SessionID != sessionID || job.TriggerPolicy == TriggerAfterAllStop {
			return Job{}, false, errors.New("plugin session index does not match job binding")
		}
		return job, true, nil
	}
	if job.TriggerPolicy != TriggerAfterAllStop || job.TargetBindingID != index.TargetBindingID ||
		index.TargetSessionHash != sessionHash {
		return Job{}, false, errors.New("barrier session index does not match job binding")
	}
	if _, found := job.StopTarget(sessionHash); !found {
		return Job{}, false, errors.New("barrier session index is not a job target")
	}
	return job, true, nil
}

func (s *Store) appendEventUnlocked(job Job, name, eventKey, sessionHash, turnHash string, oldState State) error {
	event := Event{
		SchemaVersion: "1",
		Timestamp:     s.now().UTC(),
		JobID:         job.JobID,
		Name:          name,
		EventKey:      eventKey,
		OldState:      oldState,
		NewState:      job.State,
		ReasonCode:    job.ReasonCode,
		Generation:    job.Generation,
	}
	if sessionHash == "" && job.TriggerPolicy != TriggerAfterAllStop && job.SessionID != "" {
		sessionHash = identity.SHA256([]byte(job.SessionID))
	}
	event.SessionHash = sessionHash
	event.TurnHash = turnHash
	file, err := filetrust.OpenAppendOwnerControlled(s.logPath(job.JobID), "plugin event log")
	if err != nil {
		return fmt.Errorf("open plugin event log: %w", err)
	}
	writer := bufio.NewWriter(file)
	encodeErr := json.NewEncoder(writer).Encode(event)
	flushErr := writer.Flush()
	syncErr := file.Sync()
	closeErr := file.Close()
	for _, candidate := range []error{encodeErr, flushErr, syncErr, closeErr} {
		if candidate != nil {
			return fmt.Errorf("write plugin event log: %w", candidate)
		}
	}
	return nil
}

func hasEventKey(job Job, eventKey string) bool {
	for _, existing := range job.ProcessedEventKeys {
		if existing == eventKey {
			return true
		}
	}
	return false
}

func appendEventKey(keys []string, eventKey string) []string {
	keys = append(keys, eventKey)
	if len(keys) > maxProcessedEventKeys {
		keys = append([]string(nil), keys[len(keys)-maxProcessedEventKeys:]...)
	}
	return keys
}

func appendProcessedEventKey(job *Job, eventKey string) error {
	if job.TriggerPolicy != TriggerAfterAllStop {
		job.ProcessedEventKeys = appendEventKey(job.ProcessedEventKeys, eventKey)
		return nil
	}
	if len(job.ProcessedEventKeys) >= MaximumBarrierEventKeys {
		return errors.New("barrier processed-event capacity is exhausted")
	}
	job.ProcessedEventKeys = append(job.ProcessedEventKeys, eventKey)
	return nil
}

func shouldReleaseBarrierIndexes(job Job) bool {
	return job.TriggerPolicy == TriggerAfterAllStop && job.State.IsTerminal() && !HasUnresolvedPowerAction(job)
}

func hashOptional(value string) string {
	if value == "" {
		return ""
	}
	return identity.SHA256([]byte(value))
}

func validateEventKey(eventKey string) error {
	if eventKey != "" && !ValidIdentityHash(eventKey) {
		return errors.New("invalid hook event key")
	}
	return nil
}

func validateBinding(sessionID, turnID string) error {
	if strings.TrimSpace(sessionID) == "" || len(sessionID) > 1024 {
		return errors.New("invalid hook session id")
	}
	if strings.TrimSpace(turnID) == "" || len(turnID) > 1024 {
		return errors.New("invalid hook turn id")
	}
	return nil
}

func validateTargetSessionID(sessionID string) error {
	if sessionID == "" || len(sessionID) > 1024 || strings.TrimSpace(sessionID) != sessionID {
		return errors.New("invalid target session id")
	}
	return nil
}

func (s *Store) jobsDir() string {
	return filepath.Join(s.root, "jobs")
}

func (s *Store) sessionsDir() string {
	return filepath.Join(s.root, "sessions")
}

func (s *Store) logsDir() string {
	return filepath.Join(s.root, "events")
}

func (s *Store) revocationsDir() string {
	return filepath.Join(s.root, "revocations")
}

func (s *Store) recoveryDir() string {
	return filepath.Join(s.root, "recovery")
}

func (s *Store) jobPath(jobID string) string {
	return filepath.Join(s.jobsDir(), jobID+".json")
}

func (s *Store) sessionPath(sessionID string) string {
	return filepath.Join(s.sessionsDir(), identity.SHA256([]byte(sessionID))+".json")
}

func (s *Store) sessionHashPath(sessionHash string) string {
	return filepath.Join(s.sessionsDir(), sessionHash+".json")
}

func (s *Store) logPath(jobID string) string {
	return filepath.Join(s.logsDir(), jobID+".jsonl")
}
