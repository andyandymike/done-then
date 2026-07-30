package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/store"
	"github.com/andyandymike/done-then/internal/supervisor"
)

func cancelCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printCancelUsage(streams.Stdout)
		return 0
	}
	if len(args) > 1 {
		return usageError(streams.Stderr, errors.New("cancel accepts at most one job id"))
	}
	root, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	jobStore, err := store.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize DoneThen state", err)
	}
	job, err := selectCancelableJob(jobStore, args)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitUsage, "select job", err)
	}
	if job.State == supervisor.StateCancelled {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s is already cancelled.\n", job.JobID)
		return 0
	}
	if job.State.IsTerminal() {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s is already in terminal state %s; no action is active.\n", job.JobID, job.State)
		return 0
	}
	if err := jobStore.RequestCancel(job.JobID); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "record cancellation", err)
	}
	job, err = jobStore.Load(job.JobID)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "reload job after cancellation request", err)
	}

	if !job.State.IsUnresolvedPowerState() {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s disarmed. Its Codex process is not terminated.\n", job.JobID)
		return 0
	}

	abortErr := deps.newActionBackend().AbortShutdown(ctx)
	if abortErr != nil && !errors.Is(abortErr, actions.ErrNoShutdownInProgress) {
		fmt.Fprintf(streams.Stderr, "[DoneThen] Cancellation recorded, but Windows shutdown abort failed: %v.\n", abortErr)
		fmt.Fprintf(streams.Stderr, "[DoneThen] Job %s remains in state %s; retry cancellation immediately.\n", job.JobID, job.State)
		return supervisor.ExitActionFailed
	}
	old := job.State
	if err := supervisor.Transition(old, supervisor.StateCancelled); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "transition cancelled job", err)
	}
	job.State = supervisor.StateCancelled
	job.CancelRequested = true
	job.ReasonCode = "cancelled_by_user"
	job.UpdatedAt = time.Now().UTC()
	if err := jobStore.Save(job); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist cancelled job", err)
	}
	_ = jobStore.AppendEvent(supervisor.Event{
		Timestamp: time.Now().UTC(),
		JobID:     job.JobID,
		OldState:  old,
		NewState:  job.State,
		Reason:    job.ReasonCode,
	})
	if errors.Is(abortErr, actions.ErrNoShutdownInProgress) {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s cancelled; Windows reported no shutdown currently in progress.\n", job.JobID)
	} else {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s cancelled and the Windows shutdown countdown was aborted.\n", job.JobID)
	}
	return 0
}

func selectCancelableJob(jobStore *store.Store, args []string) (supervisor.Job, error) {
	if len(args) == 1 {
		return jobStore.Load(args[0])
	}
	jobs, err := jobStore.List()
	if err != nil {
		return supervisor.Job{}, err
	}
	candidates := make([]supervisor.Job, 0)
	for _, job := range jobs {
		if !job.State.IsTerminal() {
			candidates = append(candidates, job)
		}
	}
	switch len(candidates) {
	case 0:
		return supervisor.Job{}, errors.New("no active job; provide a job id to inspect a completed job")
	case 1:
		return candidates[0], nil
	default:
		return supervisor.Job{}, fmt.Errorf("multiple active jobs; specify one of: %s", joinJobIDs(candidates))
	}
}

func joinJobIDs(jobs []supervisor.Job) string {
	value := ""
	for index, job := range jobs {
		if index != 0 {
			value += ", "
		}
		value += job.JobID
	}
	return value
}
