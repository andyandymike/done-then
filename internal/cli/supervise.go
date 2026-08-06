package cli

import (
	"context"
	"errors"

	"github.com/andyandymike/done-then/internal/hostauthority"
	"github.com/andyandymike/done-then/internal/pluginpower"
	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/powerpolicy"
	"github.com/andyandymike/done-then/internal/store"
	"github.com/andyandymike/done-then/internal/supervisor"
	"github.com/andyandymike/done-then/internal/verifierprofile"
)

func superviseCommand(ctx context.Context, args []string, streams IO, deps dependencies) int {
	if len(args) != 1 {
		return usageError(streams.Stderr, errors.New("supervise requires exactly one job id"))
	}
	if err := pluginstate.ValidateJobID(args[0]); err != nil {
		return usageError(streams.Stderr, err)
	}
	root, err := deps.dataRoot()
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "locate DoneThen data directory", err)
	}
	state, err := pluginstate.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize plugin state", err)
	}
	job, err := state.Load(args[0])
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "load plugin job", err)
	}
	classicState, err := store.New(root)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize power job inventory", err)
	}
	config := pluginpower.SupervisorConfig{
		Store:       state,
		Backend:     deps.newActionBackend(),
		AcquireLock: deps.acquirePowerLock,
		UnresolvedPowerJobs: func(currentJobID string) ([]string, error) {
			ids := make([]string, 0)
			classicJobs, listErr := classicState.ActivePowerJobs()
			if listErr != nil {
				return nil, listErr
			}
			for _, job := range classicJobs {
				ids = append(ids, job.JobID)
			}
			pluginJobs, listErr := state.List()
			if listErr != nil {
				return nil, listErr
			}
			for _, job := range pluginJobs {
				if job.JobID != currentJobID && (job.State.IsActive() || pluginstate.HasUnresolvedPowerAction(job)) && !job.DryRun {
					ids = append(ids, job.JobID)
				}
			}
			return ids, nil
		},
	}
	if job.TriggerPolicy == pluginstate.TriggerVerifiedSuccess {
		policy, loadErr := powerpolicy.Load(root)
		if loadErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "load local power policy", loadErr)
		}
		profiles, profilesErr := verifierprofile.New(root)
		if profilesErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize verifier registry", profilesErr)
		}
		proxy, proxyErr := hostauthority.StartProxyWithArgs(ctx, policy.CodexExecutable, policy.CodexPrefixArgs, Version, streams.Stderr)
		if proxyErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "connect Codex host authority", proxyErr)
		}
		defer proxy.Close()
		adapter, adapterErr := hostauthority.NewAdapter(proxy.Client(), policy.ExpectedPluginID, policy.ExpectedHookHashes)
		if adapterErr != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize Codex host authority", adapterErr)
		}
		config.Authority = adapter
		config.Profiles = profiles
		config.PolicyFingerprint = policy.Fingerprint
		config.CurrentPolicyFingerprint = func() (string, error) {
			current, currentErr := powerpolicy.Load(root)
			if currentErr != nil {
				return "", currentErr
			}
			return current.Fingerprint, nil
		}
	}
	worker, err := pluginpower.NewSupervisor(config)
	if err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "initialize one-shot supervisor", err)
	}
	if err := worker.Run(ctx, args[0]); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "run one-shot supervisor", err)
	}
	return 0
}
