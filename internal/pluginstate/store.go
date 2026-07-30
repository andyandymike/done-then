package pluginstate

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
	"time"

	"github.com/andyandymike/done-then/internal/identity"
	basestore "github.com/andyandymike/done-then/internal/store"
)

const (
	maxRecordBytes        = 1 << 20
	maxProcessedEventKeys = 128
	stateLockTimeout      = 500 * time.Millisecond
)

var ErrLockTimeout = errors.New("plugin state lock timed out")

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

type Status struct {
	JobID             string `json:"job_id"`
	State             State  `json:"state"`
	ReasonCode        string `json:"reason_code,omitempty"`
	Mode              string `json:"mode"`
	Action            string `json:"action"`
	DelaySeconds      int64  `json:"delay_seconds"`
	ExpiresAt         string `json:"expires_at"`
	Generation        uint64 `json:"generation"`
	SessionBound      bool   `json:"session_bound"`
	CompletionStatus  string `json:"completion_status,omitempty"`
	HookCompatibility string `json:"hook_compatibility"`
	ExecuteAvailable  bool   `json:"execute_available"`
	CancelCommand     string `json:"cancel_command"`
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
	for _, directory := range []string{state.jobsDir(), state.sessionsDir(), state.logsDir()} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create plugin state directory: %w", err)
		}
	}
	return state, nil
}

func (s *Store) Root() string {
	return s.root
}

func (s *Store) Create(job Job) error {
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
	return s.appendEventUnlocked(job, "mcp.arm", "", "", StateArmPendingBind)
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
		job.ProcessedEventKeys = appendEventKey(job.ProcessedEventKeys, eventKey)
	}
	job.UpdatedAt = now
	if err := Validate(job); err != nil {
		return Job{}, false, fmt.Errorf("validate updated plugin job: %w", err)
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(jobID), job); err != nil {
		return Job{}, false, err
	}
	if err := s.appendEventUnlocked(job, eventName, eventKey, "", oldState); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (s *Store) BindSession(jobID, sessionID, turnID, eventKey string) (Job, bool, error) {
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
	if err := s.appendEventUnlocked(job, "hook.post_tool.arm", eventKey, turnID, oldState); err != nil {
		return Job{}, false, err
	}
	return job, true, nil
}

func (s *Store) writeSessionIndexUnlocked(sessionID, jobID string) error {
	index := struct {
		SchemaVersion string `json:"schema_version"`
		JobID         string `json:"job_id"`
	}{SchemaVersion: "1", JobID: jobID}
	return basestore.AtomicWriteJSON(s.sessionPath(sessionID), index)
}

func (s *Store) UpdateSession(sessionID, turnID, eventName, eventKey string, mutate func(*Job, time.Time) error) (Job, bool, bool, error) {
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
	job, found, err := s.findBySessionUnlocked(sessionID)
	if err != nil || !found {
		return Job{}, false, found, err
	}
	if eventKey != "" && hasEventKey(job, eventKey) {
		return job, false, true, nil
	}
	oldState := job.State
	now := s.now().UTC()
	if err := mutate(&job, now); err != nil {
		return job, false, true, err
	}
	if eventKey != "" {
		job.ProcessedEventKeys = appendEventKey(job.ProcessedEventKeys, eventKey)
	}
	job.UpdatedAt = now
	if err := Validate(job); err != nil {
		return Job{}, false, true, fmt.Errorf("validate observed plugin job: %w", err)
	}
	if err := basestore.AtomicWriteJSON(s.jobPath(job.JobID), job); err != nil {
		return Job{}, false, true, err
	}
	if err := s.appendEventUnlocked(job, eventName, eventKey, turnID, oldState); err != nil {
		return Job{}, false, true, err
	}
	return job, true, true, nil
}

func (s *Store) Status(job Job) Status {
	mode := "execute"
	if job.DryRun {
		mode = "dry_run"
	}
	return Status{
		JobID:             job.JobID,
		State:             job.State,
		ReasonCode:        job.ReasonCode,
		Mode:              mode,
		Action:            job.Action,
		DelaySeconds:      job.DelaySeconds,
		ExpiresAt:         job.ExpiresAt.UTC().Format(time.RFC3339),
		Generation:        job.Generation,
		SessionBound:      job.SessionID != "",
		CompletionStatus:  job.CompletionStatus,
		HookCompatibility: job.HookCompatibility,
		ExecuteAvailable:  false,
		CancelCommand:     "donethen cancel " + job.JobID,
	}
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
	if job.State.IsActive() && job.Expired(now) {
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
	file, err := os.Open(path)
	if err != nil {
		return Job{}, fmt.Errorf("open plugin job %s: %w", expectedJobID, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Job{}, fmt.Errorf("inspect plugin job %s: %w", expectedJobID, err)
	}
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
	if err := Validate(job); err != nil {
		return Job{}, fmt.Errorf("plugin job %s is invalid: %w", expectedJobID, err)
	}
	return job, nil
}

func (s *Store) findBySessionUnlocked(sessionID string) (Job, bool, error) {
	file, err := os.Open(s.sessionPath(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("open plugin session index: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return Job{}, false, fmt.Errorf("inspect plugin session index: %w", err)
	}
	if info.Size() > 4096 {
		return Job{}, false, errors.New("plugin session index exceeds 4096 bytes")
	}
	var index struct {
		SchemaVersion string `json:"schema_version"`
		JobID         string `json:"job_id"`
	}
	decoder := json.NewDecoder(io.LimitReader(file, 4097))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&index); err != nil {
		return Job{}, false, fmt.Errorf("decode plugin session index: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Job{}, false, errors.New("plugin session index contains trailing data")
	}
	if index.SchemaVersion != "1" {
		return Job{}, false, errors.New("unsupported plugin session index")
	}
	job, err := s.loadPath(s.jobPath(index.JobID), index.JobID)
	if err != nil {
		return Job{}, false, err
	}
	if job.SessionID != sessionID {
		return Job{}, false, errors.New("plugin session index does not match job binding")
	}
	return job, true, nil
}

func (s *Store) appendEventUnlocked(job Job, name, eventKey, turnID string, oldState State) error {
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
	if job.SessionID != "" {
		event.SessionHash = identity.SHA256([]byte(job.SessionID))
	}
	if turnID != "" {
		event.TurnHash = identity.SHA256([]byte(turnID))
	}
	file, err := os.OpenFile(s.logPath(job.JobID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
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

func validateEventKey(eventKey string) error {
	if eventKey != "" && len(eventKey) != 64 {
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

func (s *Store) jobsDir() string {
	return filepath.Join(s.root, "jobs")
}

func (s *Store) sessionsDir() string {
	return filepath.Join(s.root, "sessions")
}

func (s *Store) logsDir() string {
	return filepath.Join(s.root, "events")
}

func (s *Store) jobPath(jobID string) string {
	return filepath.Join(s.jobsDir(), jobID+".json")
}

func (s *Store) sessionPath(sessionID string) string {
	return filepath.Join(s.sessionsDir(), identity.SHA256([]byte(sessionID))+".json")
}

func (s *Store) logPath(jobID string) string {
	return filepath.Join(s.logsDir(), jobID+".jsonl")
}
