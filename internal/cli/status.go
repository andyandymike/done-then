package cli

import (
	"errors"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/andyandymike/done-then/internal/pluginstate"
	"github.com/andyandymike/done-then/internal/store"
	"github.com/andyandymike/done-then/internal/supervisor"
)

func statusCommand(args []string, streams IO, deps dependencies) int {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		printStatusUsage(streams.Stdout)
		return 0
	}
	if len(args) > 1 {
		return usageError(streams.Stderr, errors.New("status accepts at most one job id"))
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
	var jobs []supervisor.Job
	var pluginJobs []pluginstate.Job
	if len(args) == 1 {
		job, err := jobStore.Load(args[0])
		if err == nil {
			jobs = []supervisor.Job{job}
		} else if errors.Is(err, os.ErrNotExist) {
			pluginJob, pluginErr := pluginStore.Load(args[0])
			if pluginErr != nil {
				if authority, recoveryErr := pluginStore.LoadRecoveryAuthority(args[0]); recoveryErr == nil {
					return writePluginRecoveryStatus(streams, authority.Status())
				}
				return runtimeError(streams.Stderr, supervisor.ExitStateError, "load job", pluginErr)
			}
			if pluginJob.State.IsActive() && pluginJob.Expired(timeNowUTC()) {
				pluginJob, pluginErr = pluginStore.RefreshExpiry(pluginJob.JobID)
				if pluginErr != nil {
					return runtimeError(streams.Stderr, supervisor.ExitStateError, "refresh plugin job", pluginErr)
				}
			}
			pluginJobs = []pluginstate.Job{pluginJob}
		} else {
			if authority, recoveryErr := pluginStore.LoadRecoveryAuthority(args[0]); recoveryErr == nil {
				return writePluginRecoveryStatus(streams, authority.Status())
			}
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "load job", err)
		}
	} else {
		jobs, err = jobStore.List()
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "list jobs", err)
		}
		pluginJobs, err = pluginStore.List()
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "list plugin jobs", err)
		}
	}
	if len(jobs) == 0 && len(pluginJobs) == 0 {
		fmt.Fprintln(streams.Stdout, "[DoneThen] No jobs found.")
		return 0
	}
	if len(jobs) != 0 {
		writer := tabwriter.NewWriter(streams.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "JOB ID\tSTATE\tMODE\tCANCEL\tACTION\tUPDATED\tSCHEDULED FOR")
		for _, job := range jobs {
			mode := "execute"
			if job.DryRun {
				mode = "dry-run"
			}
			scheduled := "-"
			if job.ScheduledFor != nil {
				scheduled = job.ScheduledFor.Local().Format("2006-01-02 15:04:05")
			}
			cancelled, err := jobStore.Cancelled(job.JobID)
			if err != nil {
				return runtimeError(streams.Stderr, supervisor.ExitStateError, "read cancellation state", err)
			}
			cancelState := "-"
			if cancelled {
				cancelState = "requested"
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				job.JobID,
				job.State,
				mode,
				cancelState,
				job.Action,
				job.UpdatedAt.Local().Format("2006-01-02 15:04:05"),
				scheduled,
			)
		}
		if err := writer.Flush(); err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "write status", err)
		}
	}
	if len(pluginJobs) != 0 {
		writer := tabwriter.NewWriter(streams.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(writer, "PLUGIN JOB ID\tSTATE\tTRIGGER\tMODE\tBARRIER\tCANCEL\tHOST\tVERIFIER\tACTION\tEXPIRES\tSCHEDULED")
		for _, job := range pluginJobs {
			if job.State.IsActive() && job.Expired(timeNowUTC()) {
				refreshed, refreshErr := pluginStore.RefreshExpiry(job.JobID)
				if refreshErr != nil {
					return runtimeError(streams.Stderr, supervisor.ExitStateError, "refresh plugin job", refreshErr)
				}
				job = refreshed
			}
			status := pluginStore.Status(job)
			scheduled := "-"
			if job.ScheduledFor != nil {
				scheduled = job.ScheduledFor.Local().Format("2006-01-02 15:04:05")
			}
			cancelState := "-"
			if status.CancelRequested {
				cancelState = "requested"
			}
			barrierProgress := "-"
			if status.Barrier != nil {
				barrierProgress = fmt.Sprintf("%d/%d stopped", status.Barrier.TargetsStopped, status.Barrier.TargetsTotal)
			}
			fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				status.JobID,
				status.State,
				status.TriggerPolicy,
				status.Mode,
				barrierProgress,
				cancelState,
				status.HostSnapshots,
				status.VerifierStatus,
				status.Action,
				job.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
				scheduled,
			)
		}
		if err := writer.Flush(); err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "write plugin status", err)
		}
	}
	return 0
}

var timeNowUTC = func() time.Time { return time.Now().UTC() }

func writePluginRecoveryStatus(streams IO, status pluginstate.RecoveryStatus) int {
	fmt.Fprintln(streams.Stdout, "[DoneThen] Mutable plugin job state is unavailable; independent recovery authority remains readable.")
	writer := tabwriter.NewWriter(streams.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "PLUGIN JOB ID\tRECOVERY PHASE\tRECEIPT\tSETTLEMENT REQUIRED\tCANCEL REQUIRED\tRESOLUTION\tCANCEL SCOPE")
	receipt := "intent"
	if status.ReceiptSealed {
		receipt = "sealed"
	}
	resolution := status.Resolution
	if resolution == "" {
		resolution = "-"
	}
	fmt.Fprintf(writer, "%s\t%s\t%s\t%t\t%t\t%s\t%s\n",
		status.JobID, status.Phase, receipt, status.RequiresSettlement, status.RequiresCancellation, resolution, status.CancelScope)
	if err := writer.Flush(); err != nil {
		return runtimeError(streams.Stderr, supervisor.ExitStateError, "write plugin recovery status", err)
	}
	fmt.Fprintf(streams.Stdout, "Recovery commands: %s | %s\n", status.CancelCommand, status.ReconcileCommand)
	return 0
}
