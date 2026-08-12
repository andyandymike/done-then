package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

type pluginPowerSettlement struct {
	Outcome string
	Result  *actions.CancelResult
}

// settlePluginPowerLocked decides whether cancellation may cross the backend
// boundary. The caller must hold the machine power lock: without that fence a
// no-call resolution could race a supervisor that is committing call-start.
func settlePluginPowerLocked(ctx context.Context, state *pluginstate.Store, job pluginstate.Job, backend actions.Backend, reason string) (pluginPowerSettlement, error) {
	authority, authorityErr := state.LoadRecoveryAuthority(job.JobID)
	if authorityErr == nil {
		if authority.Envelope.BindingID != pluginstate.ObservedBindingID(job) ||
			authority.Envelope.Action != job.Action || authority.Envelope.TriggerPolicy != job.TriggerPolicy {
			return pluginPowerSettlement{}, errors.New("recovery authority does not match the mutable job binding")
		}
		if authority.Resolution != nil {
			return pluginPowerSettlement{Outcome: authority.Resolution.Outcome, Result: authority.Resolution.CancelResult}, nil
		}
		if authority.Call == nil {
			resolution, err := state.PersistRecoveryResolution(job.JobID, "no_action", reason, nil, time.Now().UTC())
			if err != nil {
				return pluginPowerSettlement{}, err
			}
			return pluginPowerSettlement{Outcome: resolution.Outcome}, nil
		}
	}

	receipt, receiptErr := pluginstate.RecoveryReceipt(job)
	if authorityErr == nil {
		receipt = authority.CancellationReceipt()
		receiptErr = nil
	}
	if receiptErr != nil {
		return pluginPowerSettlement{}, receiptErr
	}
	result, cancelErr := backend.Cancel(ctx, receipt)
	settlement := pluginPowerSettlement{Result: &result}
	if cancelErr != nil && !errors.Is(cancelErr, actions.ErrNoShutdownInProgress) {
		return settlement, cancelErr
	}
	settlement.Outcome = "cancelled"
	if authorityErr == nil {
		resolution, err := state.PersistRecoveryResolution(job.JobID, "cancelled", reason, &result, time.Now().UTC())
		if err != nil {
			return settlement, err
		}
		settlement.Outcome = resolution.Outcome
		settlement.Result = resolution.CancelResult
		return settlement, nil
	}
	if !errors.Is(authorityErr, os.ErrNotExist) {
		// The backend is now inert, but an unreadable independent record must
		// remain visible as recovery-required rather than being silently ignored.
		return settlement, fmt.Errorf("power cancellation was confirmed but recovery authority is unreadable: %w", authorityErr)
	}
	return settlement, nil
}

func persistPluginPowerSettlement(state *pluginstate.Store, jobID, eventName, reason string, settlement pluginPowerSettlement) (pluginstate.Job, error) {
	if settlement.Outcome != "no_action" && settlement.Outcome != "cancelled" {
		return pluginstate.Job{}, errors.New("power settlement is not terminal")
	}
	updated, _, err := state.UpdateJob(jobID, eventName, "", func(current *pluginstate.Job, _ time.Time) error {
		if current.State.IsTerminal() && !pluginstate.HasUnresolvedPowerAction(*current) {
			return nil
		}
		if !pluginstate.HasUnresolvedPowerAction(*current) {
			return errors.New("plugin power action is no longer unresolved")
		}
		current.Generation++
		current.CancelRequested = true
		current.CancelReason = reason
		current.CancelResult = settlement.Result
		current.State = pluginstate.StateCancelled
		if settlement.Outcome == "no_action" {
			current.ReasonCode = "pre_schedule_intent_cancelled:" + reason
		} else {
			current.ReasonCode = "countdown_cancelled:" + reason
		}
		return nil
	})
	return updated, err
}
