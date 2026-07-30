package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/completion"
	"github.com/andyandymike/done-then/internal/verifier"
)

type fakeStore struct {
	job           Job
	hasJob        bool
	events        []Event
	cancelChecks  int
	cancelAt      int
	failSaveState State
}

func (s *fakeStore) Create(job Job) error {
	if s.hasJob {
		return errors.New("already created")
	}
	s.job = job
	s.hasJob = true
	return nil
}

func (s *fakeStore) Save(job Job) error {
	if job.State == s.failSaveState && s.failSaveState != "" {
		return errors.New("injected save failure")
	}
	s.job = job
	return nil
}

func (s *fakeStore) Cancelled(string) (bool, error) {
	s.cancelChecks++
	return s.cancelAt > 0 && s.cancelChecks >= s.cancelAt, nil
}

func (s *fakeStore) AppendEvent(event Event) error {
	s.events = append(s.events, event)
	return nil
}

type fakeAgent struct {
	result AgentResult
	err    error
}

func (a fakeAgent) Run(context.Context) (AgentResult, error) {
	return a.result, a.err
}

type fakeVerifier struct {
	result verifier.Result
	err    error
}

func (v fakeVerifier) Run(context.Context) (verifier.Result, error) {
	return v.result, v.err
}

func TestCoordinatorDryRunPassesWithoutAction(t *testing.T) {
	jobStore := &fakeStore{}
	backend := &actions.FakeBackend{}
	coordinator := newTestCoordinator(t, Config{
		DryRun:  true,
		Agent:   successfulAgent(t, completion.StatusDone),
		Backend: backend,
		Store:   jobStore,
	})
	outcome := coordinator.Run(context.Background())
	if outcome.ExitCode != ExitOK || outcome.State != StateDryRunComplete {
		t.Fatalf("Run() = %#v", outcome)
	}
	scheduleCalls, _, _, _ := backend.Snapshot()
	if scheduleCalls != 0 {
		t.Fatalf("ScheduleShutdown calls = %d", scheduleCalls)
	}
	if jobStore.job.State != StateDryRunComplete {
		t.Fatalf("persisted state = %s", jobStore.job.State)
	}
}

func TestCoordinatorFailClosedOutcomes(t *testing.T) {
	tests := []struct {
		name      string
		agent     Agent
		verifier  Verifier
		wantCode  int
		wantState State
	}{
		{
			name:      "partial",
			agent:     successfulAgent(t, completion.StatusPartial),
			wantCode:  ExitNotDone,
			wantState: StateNotDone,
		},
		{
			name: "invalid completion",
			agent: fakeAgent{result: AgentResult{
				ExitCode:      0,
				CompletionErr: errors.New("missing final response"),
			}},
			wantCode:  ExitInvalidCompletion,
			wantState: StateInvalidCompletion,
		},
		{
			name:      "agent failed",
			agent:     fakeAgent{result: AgentResult{ExitCode: 7}, err: errors.New("Codex exited")},
			wantCode:  ExitAgentFailed,
			wantState: StateAgentFailed,
		},
		{
			name:      "verifier failed",
			agent:     successfulAgent(t, completion.StatusDone),
			verifier:  fakeVerifier{result: verifier.Result{ExitCode: 3}, err: errors.New("verifier failed")},
			wantCode:  ExitVerificationFailed,
			wantState: StateVerificationFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jobStore := &fakeStore{}
			backend := &actions.FakeBackend{}
			coordinator := newTestCoordinator(t, Config{
				DryRun:   true,
				Agent:    test.agent,
				Verifier: test.verifier,
				Backend:  backend,
				Store:    jobStore,
			})
			outcome := coordinator.Run(context.Background())
			if outcome.ExitCode != test.wantCode || outcome.State != test.wantState {
				t.Fatalf("Run() = %#v, want code=%d state=%s", outcome, test.wantCode, test.wantState)
			}
			scheduleCalls, _, _, _ := backend.Snapshot()
			if scheduleCalls != 0 {
				t.Fatalf("ScheduleShutdown calls = %d", scheduleCalls)
			}
		})
	}
}

func TestCoordinatorSchedulesExactlyOnceAfterAllGates(t *testing.T) {
	jobStore := &fakeStore{}
	backend := &actions.FakeBackend{}
	coordinator := newTestCoordinator(t, Config{
		DryRun:   false,
		Agent:    successfulAgent(t, completion.StatusDone),
		Verifier: fakeVerifier{result: verifier.Result{ExitCode: 0}},
		Backend:  backend,
		Store:    jobStore,
	})
	outcome := coordinator.Run(context.Background())
	if outcome.ExitCode != ExitOK || outcome.State != StateActionScheduled || !outcome.ActionMayBeScheduled {
		t.Fatalf("Run() = %#v", outcome)
	}
	scheduleCalls, _, delay, comment := backend.Snapshot()
	if scheduleCalls != 1 || delay != 2*time.Minute {
		t.Fatalf("backend calls=%d delay=%s", scheduleCalls, delay)
	}
	if strings.Contains(comment, "model summary") || !strings.Contains(comment, "dt_TEST") {
		t.Fatalf("unsafe or unexpected shutdown comment %q", comment)
	}
	if jobStore.job.State != StateActionScheduled || jobStore.job.ScheduledFor == nil {
		t.Fatalf("persisted job = %#v", jobStore.job)
	}
}

func TestCoordinatorCancellationWinsAtActionBoundary(t *testing.T) {
	jobStore := &fakeStore{cancelAt: 4}
	backend := &actions.FakeBackend{}
	coordinator := newTestCoordinator(t, Config{
		DryRun:  false,
		Agent:   successfulAgent(t, completion.StatusDone),
		Backend: backend,
		Store:   jobStore,
	})
	outcome := coordinator.Run(context.Background())
	if outcome.ExitCode != ExitCancelled || outcome.State != StateCancelled {
		t.Fatalf("Run() = %#v", outcome)
	}
	scheduleCalls, _, _, _ := backend.Snapshot()
	if scheduleCalls != 0 {
		t.Fatalf("ScheduleShutdown calls = %d", scheduleCalls)
	}
}

func TestCoordinatorCancellationAfterScheduleIsAborted(t *testing.T) {
	jobStore := &fakeStore{cancelAt: 5}
	backend := &actions.FakeBackend{}
	coordinator := newTestCoordinator(t, Config{
		DryRun:  false,
		Agent:   successfulAgent(t, completion.StatusDone),
		Backend: backend,
		Store:   jobStore,
	})
	outcome := coordinator.Run(context.Background())
	if outcome.ExitCode != ExitCancelled || outcome.State != StateCancelled {
		t.Fatalf("Run() = %#v", outcome)
	}
	scheduleCalls, abortCalls, _, _ := backend.Snapshot()
	if scheduleCalls != 1 || abortCalls != 1 {
		t.Fatalf("backend schedule=%d abort=%d", scheduleCalls, abortCalls)
	}
}

func TestCoordinatorReportsUncertainActionWhenFinalPersistenceFails(t *testing.T) {
	jobStore := &fakeStore{failSaveState: StateActionScheduled}
	backend := &actions.FakeBackend{}
	coordinator := newTestCoordinator(t, Config{
		DryRun:  false,
		Agent:   successfulAgent(t, completion.StatusDone),
		Backend: backend,
		Store:   jobStore,
	})
	outcome := coordinator.Run(context.Background())
	if outcome.ExitCode != ExitStateError || !outcome.ActionMayBeScheduled {
		t.Fatalf("Run() = %#v", outcome)
	}
	scheduleCalls, _, _, _ := backend.Snapshot()
	if scheduleCalls != 1 {
		t.Fatalf("ScheduleShutdown calls = %d", scheduleCalls)
	}
	if jobStore.job.State != StateActionIntentRecorded {
		t.Fatalf("persisted state = %s", jobStore.job.State)
	}
}

func newTestCoordinator(t *testing.T, overrides Config) *Coordinator {
	t.Helper()
	config := overrides
	config.JobID = "dt_TESTJOB123456"
	config.NonceHash = "sha256:test"
	config.Action = "shutdown"
	config.Delay = 2 * time.Minute
	config.CodexCWD = t.TempDir()
	config.PromptSHA256 = "prompt-hash"
	config.Now = func() time.Time { return time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC) }
	coordinator, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func successfulAgent(t *testing.T, status completion.Status) Agent {
	t.Helper()
	envelope := completion.Envelope{
		SchemaVersion: "1",
		Status:        status,
		Summary:       "model summary",
		Checks: []completion.Check{{
			Name:     "test",
			Status:   completion.CheckPassed,
			Evidence: "exit 0",
		}},
		RemainingWork:    []string{},
		ApprovalRequired: false,
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return fakeAgent{result: AgentResult{ExitCode: 0, PID: 1234, Completion: data}}
}
