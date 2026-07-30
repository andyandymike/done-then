package cli

import (
	"errors"
	"fmt"
	"text/tabwriter"

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
	var jobs []supervisor.Job
	if len(args) == 1 {
		job, err := jobStore.Load(args[0])
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "load job", err)
		}
		jobs = []supervisor.Job{job}
	} else {
		jobs, err = jobStore.List()
		if err != nil {
			return runtimeError(streams.Stderr, supervisor.ExitStateError, "list jobs", err)
		}
	}
	if len(jobs) == 0 {
		fmt.Fprintln(streams.Stdout, "[DoneThen] No jobs found.")
		return 0
	}
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
	return 0
}
