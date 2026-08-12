package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/pluginstate"
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
	pluginStore, err := pluginstate.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize DoneThen plugin state", err)
	}
	if len(args) == 1 {
		job, loadErr := jobStore.Load(args[0])
		if loadErr == nil {
			return cancelSupervisorJob(ctx, job, streams, deps, jobStore)
		}
		if !errors.Is(loadErr, os.ErrNotExist) {
			if authority, recoveryErr := pluginStore.LoadRecoveryAuthority(args[0]); recoveryErr == nil {
				return cancelPluginRecoveryAuthority(ctx, authority, streams, deps, pluginStore)
			}
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "load job", loadErr)
		}
		pluginJob, pluginErr := pluginStore.Load(args[0])
		if pluginErr != nil {
			if authority, recoveryErr := pluginStore.LoadRecoveryAuthority(args[0]); recoveryErr == nil {
				return cancelPluginRecoveryAuthority(ctx, authority, streams, deps, pluginStore)
			}
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "load job", pluginErr)
		}
		return cancelPluginJob(ctx, pluginJob, streams, deps, pluginStore)
	}

	jobs, err := jobStore.List()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "list jobs", err)
	}
	pluginJobs, err := pluginStore.List()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "list plugin jobs", err)
	}
	classicCandidates := make([]supervisor.Job, 0)
	pluginCandidates := make([]pluginstate.Job, 0)
	ids := make([]string, 0)
	for _, job := range jobs {
		if !job.State.IsTerminal() {
			classicCandidates = append(classicCandidates, job)
			ids = append(ids, job.JobID)
		}
	}
	for _, job := range pluginJobs {
		if job.State.IsActive() || pluginstate.HasUnresolvedPowerAction(job) {
			pluginCandidates = append(pluginCandidates, job)
			ids = append(ids, job.JobID)
		}
	}
	switch len(ids) {
	case 0:
		return runtimeError(streams.Stderr, supervisor.ExitUsage, "select job", errors.New("no active job; provide a job id to inspect a completed job"))
	case 1:
		if len(classicCandidates) == 1 {
			return cancelSupervisorJob(ctx, classicCandidates[0], streams, deps, jobStore)
		}
		return cancelPluginJob(ctx, pluginCandidates[0], streams, deps, pluginStore)
	default:
		return runtimeError(streams.Stderr, supervisor.ExitUsage, "select job", fmt.Errorf("multiple active jobs; specify one of: %s", strings.Join(ids, ", ")))
	}
}

func cancelPluginRecoveryAuthority(ctx context.Context, authority pluginstate.RecoveryAuthority, streams IO, deps dependencies, pluginStore *pluginstate.Store) int {
	lock, err := deps.acquirePowerLock()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "acquire recovery cancellation fence", err)
	}
	defer lock.Release()
	authority, err = pluginStore.LoadRecoveryAuthority(authority.Envelope.JobID)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "reload plugin recovery authority", err)
	}
	if authority.Resolution != nil {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Recovery authority for plugin job %s is already resolved as %s.\n", authority.Envelope.JobID, authority.Resolution.Outcome)
		return 0
	}
	if authority.Call == nil {
		if _, err := pluginStore.PersistRecoveryResolution(authority.Envelope.JobID, "no_action", "cli_recovered_before_schedule_call", nil, time.Now().UTC()); err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist pre-call recovery resolution", err)
		}
		fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s had no durable schedule-call start; it was resolved without calling the power backend.\n", authority.Envelope.JobID)
		return 0
	}
	receipt := authority.CancellationReceipt()
	result, cancelErr := deps.newActionBackend().Cancel(ctx, receipt)
	if cancelErr != nil && !errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
		return runtimeError(streams.Stderr, supervisor.ExitActionFailed, "cancel from plugin recovery authority", cancelErr)
	}
	if _, err := pluginStore.PersistRecoveryResolution(authority.Envelope.JobID, "cancelled", "cli_recovered_without_job_projection", &result, time.Now().UTC()); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist recovery cancellation resolution", err)
	}
	fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s cancellation was confirmed from its independent recovery authority (scope=%s).\n", authority.Envelope.JobID, result.Scope)
	return 0
}

func cancelSupervisorJob(ctx context.Context, job supervisor.Job, streams IO, deps dependencies, jobStore *store.Store) int {
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
	reloaded, err := jobStore.Load(job.JobID)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "reload job after cancellation request", err)
	}
	job = reloaded
	intentAtRequest := job.State == supervisor.StateActionIntentRecorded

	if !job.State.IsUnresolvedPowerState() {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s disarmed. Its Codex process is not terminated.\n", job.JobID)
		return 0
	}

	receipt := cancellationReceipt(job)
	cancelResult, cancelErr := deps.newActionBackend().Cancel(ctx, receipt)
	job.CancelResult = &cancelResult
	if cancelErr != nil && !errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
		fmt.Fprintf(streams.Stderr, "[DoneThen] Cancellation recorded, but the power backend could not cancel the countdown: %v.\n", cancelErr)
		fmt.Fprintf(streams.Stderr, "[DoneThen] Job %s remains in state %s; retry cancellation immediately.\n", job.JobID, job.State)
		return supervisor.ExitActionFailed
	}
	if intentAtRequest {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s has a persisted cancellation request at an unresolved scheduler boundary; retry status/cancel until the coordinator settles it.\n", job.JobID)
		return 0
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
	if errors.Is(cancelErr, actions.ErrNoShutdownInProgress) || cancelResult.NoActionInProgress {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s cancelled; the backend reported no shutdown currently in progress.\n", job.JobID)
	} else {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s cancelled and the shutdown countdown was aborted (scope=%s).\n", job.JobID, cancelResult.Scope)
	}
	return 0
}

func cancellationReceipt(job supervisor.Job) actions.Receipt {
	if job.PowerReceipt != nil {
		return *job.PowerReceipt
	}
	requestedAt := job.CreatedAt.UTC()
	if job.ActionIntentAt != nil {
		requestedAt = job.ActionIntentAt.UTC()
	}
	deadline := requestedAt.Add(time.Duration(job.DelaySeconds) * time.Second)
	if job.ScheduledFor != nil {
		deadline = job.ScheduledFor.UTC()
	}
	platformName := "windows"
	backendID := "windows-shutdown-exe"
	cancelScope := actions.CancelScopeSystemGlobal
	if job.PowerCapabilities != nil {
		platformName = job.PowerCapabilities.Platform
		backendID = job.PowerCapabilities.BackendID
		cancelScope = job.PowerCapabilities.CancelScope
	}
	externalToken := ""
	if platformName == "linux-systemd" && backendID == "linux-systemd-helper" {
		externalToken = actions.SystemdUnitToken(job.JobID)
	}
	return actions.SealReceipt(actions.Receipt{
		SchemaVersion:  actions.ReceiptSchemaVersion,
		Platform:       platformName,
		BackendID:      backendID,
		BackendVersion: "intent-recovery",
		JobID:          job.JobID,
		Action:         job.Action,
		RequestedAt:    requestedAt,
		ScheduledAt:    requestedAt,
		Deadline:       deadline,
		CancelScope:    cancelScope,
		ExternalToken:  externalToken,
		ResultSummary:  "reconstructed from unresolved action intent",
	})
}

func cancelPluginJob(ctx context.Context, job pluginstate.Job, streams IO, deps dependencies, pluginStore *pluginstate.Store) int {
	if job.State == pluginstate.StateCancelled {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s is already cancelled.\n", job.JobID)
		return 0
	}
	unresolvedPower := pluginstate.HasUnresolvedPowerAction(job)
	if job.State.IsTerminal() && !unresolvedPower {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s is already in terminal state %s; no action is active.\n", job.JobID, job.State)
		return 0
	}
	var cancelResult *actions.CancelResult
	if unresolvedPower {
		job, _, err := pluginStore.UpdateJob(job.JobID, "cli.cancel.requested", "", func(current *pluginstate.Job, _ time.Time) error {
			if !pluginstate.HasUnresolvedPowerAction(*current) {
				return nil
			}
			current.Generation++
			current.CancelRequested = true
			current.CancelReason = "cli_cancelled_by_user"
			current.ReasonCode = "power_cancel_requested"
			return nil
		})
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist plugin cancellation request", err)
		}
		powerLock, lockErr := deps.acquirePowerLock()
		if lockErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "acquire plugin cancellation fence after persisting the request", lockErr)
		}
		settlement, settleErr := settlePluginPowerLocked(ctx, pluginStore, job, deps.newActionBackend(), "cli_cancelled_by_user")
		if settleErr != nil {
			_ = powerLock.Release()
			fmt.Fprintf(streams.Stderr, "[DoneThen] Plugin cancellation remains recovery-required: %v. Retry immediately.\n", settleErr)
			return supervisor.ExitActionFailed
		}
		updated, persistErr := persistPluginPowerSettlement(pluginStore, job.JobID, "cli.cancel.settled", "cli_cancelled_by_user", settlement)
		releaseErr := powerLock.Release()
		if persistErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist settled plugin cancellation", persistErr)
		}
		if releaseErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "release plugin cancellation fence", releaseErr)
		}
		cancelResult = settlement.Result
		if settlement.Outcome == "no_action" {
			fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s cancelled before the power backend was called.\n", updated.JobID)
		} else {
			fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s cancelled; the countdown was confirmed inert (scope=%s).\n", updated.JobID, cancelResult.Scope)
		}
		return 0
	}
	updated, _, err := pluginStore.UpdateJob(job.JobID, "cli.cancel", "", func(job *pluginstate.Job, _ time.Time) error {
		if job.State.IsTerminal() && !pluginstate.HasUnresolvedPowerAction(*job) {
			return nil
		}
		job.Generation++
		job.State = pluginstate.StateCancelled
		job.ReasonCode = "cancelled_by_user"
		job.CancelRequested = true
		job.CancelReason = "cli_cancelled_by_user"
		job.CancelResult = cancelResult
		job.CompletionStatus = ""
		job.CompletionEvidenceHash = ""
		job.ReadyTurnID = ""
		job.StopTurnID = ""
		job.FinishObserved = false
		return nil
	})
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "cancel plugin job", err)
	}
	if updated.State != pluginstate.StateCancelled {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s became terminal in state %s; no action is active.\n", updated.JobID, updated.State)
		return 0
	}
	if cancelResult != nil {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s cancelled; the countdown was aborted (scope=%s).\n", updated.JobID, cancelResult.Scope)
	} else if updated.DryRun {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s cancelled. Observe-only plugin mode had not scheduled a power action.\n", updated.JobID)
	} else {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s cancelled before a power action was scheduled.\n", updated.JobID)
	}
	return 0
}
