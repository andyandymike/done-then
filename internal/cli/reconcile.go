package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/store"
	"github.com/andyandymike/done-then/internal/supervisor"
)

func reconcileCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printReconcileUsage(streams.Stdout)
		return 0
	}
	if len(args) != 1 {
		return usageError(streams.Stderr, errors.New("reconcile requires exactly one job id"))
	}
	root, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	jobStore, err := store.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize DoneThen state", err)
	}
	job, err := jobStore.Load(args[0])
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "load job", err)
		}
		pluginStore, pluginErr := pluginstate.New(root)
		if pluginErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize plugin state", pluginErr)
		}
		pluginJob, pluginErr := pluginStore.Load(args[0])
		if pluginErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "load job", pluginErr)
		}
		return reconcilePluginJob(ctx, pluginJob, streams, deps, pluginStore)
	}
	if job.State != supervisor.StateActionIntentRecorded && job.State != supervisor.StateActionScheduled &&
		job.State != supervisor.StateActionExecutionUnverified &&
		job.State != supervisor.StateActionExecutedConfirmed {
		return runtimeError(streams.Stderr, supervisor.ExitUsage, "reconcile job", fmt.Errorf("state %s has no scheduled power action to reconcile", job.State))
	}
	receipt := cancellationReceipt(job)
	result, err := deps.newActionBackend().Reconcile(ctx, receipt)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitActionFailed, "reconcile power receipt", err)
	}
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now().UTC()
	}
	old := job.State
	reasonCode := string(result.State)
	switch result.State {
	case actions.ReconcileScheduled:
		// Keep ACTION_SCHEDULED. Reconciliation is read-only and the countdown
		// or unresolved intent may still be cancellable.
	case actions.ReconcileUnverified:
		if (job.State == supervisor.StateActionScheduled || job.State == supervisor.StateActionIntentRecorded) && result.CheckedAt.Before(receipt.Deadline) {
			reasonCode = "execution_unverified_before_deadline"
		} else if job.State == supervisor.StateActionScheduled || job.State == supervisor.StateActionIntentRecorded {
			if err := supervisor.Transition(job.State, supervisor.StateActionExecutionUnverified); err != nil {
				return runtimeError(streams.Stderr, supervisor.ExitStateError, "transition reconciled job", err)
			}
			job.State = supervisor.StateActionExecutionUnverified
		}
	case actions.ReconcileConfirmed:
		if job.State != supervisor.StateActionExecutedConfirmed {
			if err := supervisor.Transition(job.State, supervisor.StateActionExecutedConfirmed); err != nil {
				return runtimeError(streams.Stderr, supervisor.ExitStateError, "transition reconciled job", err)
			}
			job.State = supervisor.StateActionExecutedConfirmed
		}
	default:
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "reconcile power receipt", fmt.Errorf("backend returned unknown reconcile state %q", result.State))
	}
	if job.PowerReceipt == nil {
		job.PowerReceipt = &receipt
	}
	if job.ScheduledFor == nil {
		deadline := receipt.Deadline.UTC()
		job.ScheduledFor = &deadline
	}
	job.ReconcileResult = &result
	job.ReasonCode = reasonCode
	job.UpdatedAt = result.CheckedAt.UTC()
	if err := jobStore.Save(job); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist reconciliation", err)
	}
	if old != job.State {
		_ = jobStore.AppendEvent(supervisor.Event{
			Timestamp: job.UpdatedAt,
			JobID:     job.JobID,
			OldState:  old,
			NewState:  job.State,
			Reason:    job.ReasonCode,
		})
	}
	fmt.Fprintf(streams.Stdout, "[DoneThen] Job %s reconcile=%s state=%s.\n", job.JobID, result.State, job.State)
	if result.Evidence != "" {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Evidence: %s.\n", result.Evidence)
	}
	return 0
}

func reconcilePluginJob(ctx context.Context, job pluginstate.Job, streams IO, deps dependencies, state *pluginstate.Store) int {
	if !pluginstate.HasUnresolvedPowerAction(job) && job.State != pluginstate.StateActionExecutionUnverified &&
		job.State != pluginstate.StateActionExecutedConfirmed {
		return runtimeError(streams.Stderr, supervisor.ExitUsage, "reconcile plugin job", fmt.Errorf("state %s has no scheduled power action to reconcile", job.State))
	}
	receipt, err := pluginstate.RecoveryReceipt(job)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "recover plugin reconciliation receipt", err)
	}
	result, err := deps.newActionBackend().Reconcile(ctx, receipt)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitActionFailed, "reconcile plugin power receipt", err)
	}
	if result.CheckedAt.IsZero() {
		result.CheckedAt = time.Now().UTC()
	}
	updated, _, err := state.UpdateJob(job.JobID, "power.reconcile", "", func(current *pluginstate.Job, _ time.Time) error {
		keepCancellable := false
		currentReceipt, receiptErr := pluginstate.RecoveryReceipt(*current)
		if receiptErr != nil || currentReceipt.Checksum != receipt.Checksum {
			return errors.New("plugin power receipt changed during reconciliation")
		}
		if current.PowerReceipt == nil {
			current.PowerReceipt = &receipt
		}
		if current.ScheduledFor == nil {
			deadline := receipt.Deadline.UTC()
			current.ScheduledFor = &deadline
		}
		switch result.State {
		case actions.ReconcileScheduled:
			if !pluginstate.HasUnresolvedPowerAction(*current) && current.State != pluginstate.StateActionScheduled {
				return errors.New("backend cannot report an unrelated plugin job as scheduled")
			}
		case actions.ReconcileUnverified:
			if (pluginstate.HasUnresolvedPowerAction(*current) || current.State == pluginstate.StateActionScheduled) && result.CheckedAt.Before(receipt.Deadline) {
				current.ReasonCode = "execution_unverified_before_deadline"
				keepCancellable = true
			} else if pluginstate.HasUnresolvedPowerAction(*current) || current.State == pluginstate.StateActionScheduled {
				current.State = pluginstate.StateActionExecutionUnverified
			}
		case actions.ReconcileConfirmed:
			current.State = pluginstate.StateActionExecutedConfirmed
		default:
			return fmt.Errorf("backend returned unknown reconcile state %q", result.State)
		}
		current.ReconcileResult = &result
		if !keepCancellable {
			current.ReasonCode = string(result.State)
		}
		return nil
	})
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist plugin reconciliation", err)
	}
	fmt.Fprintf(streams.Stdout, "[DoneThen] Plugin job %s reconcile=%s state=%s.\n", updated.JobID, result.State, updated.State)
	if result.Evidence != "" {
		fmt.Fprintf(streams.Stdout, "[DoneThen] Evidence: %s.\n", result.Evidence)
	}
	return 0
}
