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
	"github.com/andyandymike/done-then/internal/supervisor"
)

// cancelWorkerCommand is an internal detached recovery path. It can only
// reduce authority: it either fences a pre-intent job or cancels a
// receipt-bound power action. It must never call Backend.Schedule.
func cancelWorkerCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) != 3 {
		return usageError(streams.Stderr, errors.New("cancel-worker requires job id, binding id, and reason"))
	}
	jobID, bindingID, reason := args[0], args[1], args[2]
	if err := pluginstate.ValidateJobID(jobID); err != nil {
		return usageError(streams.Stderr, err)
	}
	if err := pluginstate.ValidateJobID(bindingID); err != nil {
		return usageError(streams.Stderr, errors.New("invalid revocation binding identity"))
	}
	if strings.TrimSpace(reason) == "" || len(reason) > 256 {
		return usageError(streams.Stderr, errors.New("invalid cancel-worker reason"))
	}
	root, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	state, err := pluginstate.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize plugin state", err)
	}
	powerLock, err := deps.acquirePowerLock()
	if err != nil {
		job, loadErr := state.Load(jobID)
		retryUntil := time.Now().UTC().Add(30 * time.Second)
		if loadErr == nil && pluginstate.ObservedBindingID(job) == bindingID &&
			(!job.State.IsTerminal() || pluginstate.HasUnresolvedPowerAction(job)) {
			_, _, _ = state.UpdateJob(jobID, "cancel_worker.lock_unavailable", "", func(current *pluginstate.Job, _ time.Time) error {
				if pluginstate.ObservedBindingID(*current) == bindingID && (!current.State.IsTerminal() || pluginstate.HasUnresolvedPowerAction(*current)) {
					current.Generation++
					current.CancelRequested = true
					current.CancelReason = reason
					current.ReasonCode = "cancel_worker_unavailable:machine_power_lock_held"
				}
				return nil
			})
			if pluginstate.HasUnresolvedPowerAction(job) {
				if receipt, receiptErr := pluginstate.RecoveryReceipt(job); receiptErr == nil {
					retryUntil = receipt.Deadline.UTC().Add(5 * time.Minute)
				}
			} else if job.ExpiresAt.After(retryUntil) {
				retryUntil = job.ExpiresAt.UTC()
			}
		}
		maximumRetryUntil := time.Now().UTC().Add(65 * time.Minute)
		if retryUntil.After(maximumRetryUntil) {
			retryUntil = maximumRetryUntil
		}
		for err != nil && time.Now().UTC().Before(retryUntil) {
			timer := time.NewTimer(250 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return runtimeError(streams.Stderr, supervisor.ExitStateError, "wait for cancel-worker power fence", ctx.Err())
			case <-timer.C:
			}
			powerLock, err = deps.acquirePowerLock()
		}
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "acquire cancel-worker power fence", err)
		}
	}
	defer powerLock.Release()

	job, err := state.Load(jobID)
	if err != nil {
		authority, recoveryErr := state.LoadRecoveryAuthority(jobID)
		if recoveryErr == nil && authority.Envelope.BindingID == bindingID {
			if authority.Resolution != nil {
				return 0
			}
			if authority.Call == nil {
				_, recoveryErr = state.PersistRecoveryResolution(jobID, "no_action", reason, nil, time.Now().UTC())
				if recoveryErr == nil {
					return 0
				}
			} else {
				receipt := authority.CancellationReceipt()
				result, cancelErr := deps.newActionBackend().Cancel(ctx, receipt)
				if cancelErr == nil || errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
					_, recoveryErr = state.PersistRecoveryResolution(jobID, "cancelled", reason, &result, time.Now().UTC())
					if recoveryErr == nil {
						return 0
					}
				} else {
					recoveryErr = cancelErr
				}
			}
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "settle cancel-worker recovery authority", recoveryErr)
		}
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "load revocation job", err)
	}
	if pluginstate.ObservedBindingID(job) != bindingID {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "validate revocation binding", errors.New("cancel-worker binding no longer matches"))
	}
	if job.State.IsTerminal() && !pluginstate.HasUnresolvedPowerAction(job) {
		return 0
	}
	if !pluginstate.HasUnresolvedPowerAction(job) {
		if err := persistPreIntentRevocationFence(ctx, state, job, bindingID, reason); err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist cancel-worker fence", err)
		}
		if err := state.CompletePendingRevocations(jobID, bindingID); err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "complete revocation requests", err)
		}
		return 0
	}
	settlement, settleErr := settlePluginPowerLocked(ctx, state, job, deps.newActionBackend(), reason)
	if settleErr != nil {
		_, _, _ = state.UpdateJob(job.JobID, "cancel_worker.receipt_unavailable", "", func(current *pluginstate.Job, _ time.Time) error {
			if pluginstate.HasUnresolvedPowerAction(*current) {
				current.CancelRequested = true
				current.CancelReason = reason
				current.ReasonCode = "countdown_cancel_unconfirmed:" + reason
				current.CancelResult = settlement.Result
			}
			return nil
		})
		fmt.Fprintf(streams.Stderr, "[DoneThen cancel-worker %d] cancellation remains unconfirmed: %v.\n", os.Getpid(), settleErr)
		return supervisor.ExitActionFailed
	}
	_, stateErr := persistPluginPowerSettlement(state, job.JobID, "cancel_worker.cancel", reason, settlement)
	if stateErr != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "persist cancel-worker result", stateErr)
	}
	if err := state.CompletePendingRevocations(jobID, bindingID); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "complete revocation requests", err)
	}
	return 0
}

// persistPreIntentRevocationFence deliberately keeps the machine power lock held
// while the owner-controlled state store is temporarily unavailable. Releasing
// the lock before the barrier is inert would allow the original supervisor to
// race forward after the revocation channel recovers.
func persistPreIntentRevocationFence(ctx context.Context, state *pluginstate.Store, initial pluginstate.Job, bindingID, reason string) error {
	expiresAt := initial.ExpiresAt.UTC()
	for {
		updated, _, updateErr := state.UpdateJob(initial.JobID, "cancel_worker.fence", "", func(current *pluginstate.Job, _ time.Time) error {
			if pluginstate.ObservedBindingID(*current) != bindingID {
				return errors.New("cancel-worker binding no longer matches")
			}
			if current.State.IsTerminal() && !pluginstate.HasUnresolvedPowerAction(*current) {
				return nil
			}
			if pluginstate.HasUnresolvedPowerAction(*current) {
				return errors.New("barrier crossed the power-intent boundary while fenced")
			}
			current.Generation++
			current.CancelRequested = true
			current.CancelReason = reason
			current.State = pluginstate.StateCancelled
			current.ReasonCode = "revocation_channel_unavailable:" + reason
			return nil
		})
		if updateErr == nil && updated.State.IsTerminal() && !pluginstate.HasUnresolvedPowerAction(updated) {
			return nil
		}
		// Atomic state persistence may have succeeded even when event logging or
		// index cleanup failed. A terminal reload is enough to keep power off.
		if current, loadErr := state.Load(initial.JobID); loadErr == nil {
			if pluginstate.ObservedBindingID(current) != bindingID {
				return errors.New("cancel-worker binding changed while fenced")
			}
			expiresAt = current.ExpiresAt.UTC()
			if current.State.IsTerminal() && !pluginstate.HasUnresolvedPowerAction(current) {
				return nil
			}
		}
		if !expiresAt.IsZero() && !time.Now().UTC().Before(expiresAt) {
			return errors.New("barrier expired while its fail-closed fence could not be persisted")
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
