//go:build linux

package powerdaemon

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/andyandymike/done-then/internal/actions"
)

type recordingRunner struct {
	mu    sync.Mutex
	calls [][]string
	err   error
}

type runnerResponse struct {
	exitCode int
	output   string
	err      error
}

type scriptedRunner struct {
	calls     [][]string
	responses []runnerResponse
}

func (r *scriptedRunner) Run(_ context.Context, executable string, args ...string) (int, []byte, error) {
	r.calls = append(r.calls, append([]string{executable}, args...))
	if len(r.responses) == 0 {
		return 0, nil, nil
	}
	response := r.responses[0]
	r.responses = r.responses[1:]
	return response.exitCode, []byte(response.output), response.err
}

func (r *recordingRunner) Run(_ context.Context, executable string, args ...string) (int, []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := append([]string{executable}, args...)
	r.calls = append(r.calls, call)
	if r.err != nil {
		return 1, nil, r.err
	}
	return 0, nil, nil
}

func TestHelperUsesFixedSystemdTimerAndJobSpecificCancel(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{}
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	config := withDefaults(Config{
		StateDirectory: root,
		Now:            func() time.Time { return now },
		Runner:         runner,
		HostCheck:      func() error { return nil },
	})
	s := &server{config: config}
	jobID := "dt_linux_helper_test"
	request := actions.PowerRequest{
		JobID: jobID, Action: "shutdown", Delay: 2 * time.Minute,
		Comment: actions.PluginPowerComment(jobID), RequestedAt: now,
	}
	response := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "schedule", Request: &request})
	if !response.OK || response.Receipt == nil {
		t.Fatalf("schedule response = %#v", response)
	}
	if err := actions.ValidateReceipt(*response.Receipt); err != nil {
		t.Fatal(err)
	}
	token := actions.SystemdUnitToken(jobID)
	first := strings.Join(runner.calls[0], " ")
	for _, required := range []string{"/usr/bin/systemd-run", "--unit=" + token, "--on-active=120s", "/usr/local/libexec/donethen/donethen-powerd", "--fire-job=" + jobID} {
		if !strings.Contains(first, required) {
			t.Fatalf("systemd-run call %q does not contain %q", first, required)
		}
	}
	cancel := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "cancel", Receipt: response.Receipt})
	if !cancel.OK || cancel.CancelResult == nil || !cancel.CancelResult.Cancelled || cancel.CancelResult.Scope != actions.CancelScopeJob {
		t.Fatalf("cancel response = %#v", cancel)
	}
	if _, err := os.Stat(s.activePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("active helper state remains after cancel: %v", err)
	}
}

func TestHelperKeepsRecoveryStateWhenSystemdSchedulingIsUncertain(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{err: errors.New("injected systemd disconnect")}
	config := withDefaults(Config{StateDirectory: root, Runner: runner, HostCheck: func() error { return nil }})
	s := &server{config: config}
	now := time.Now().UTC()
	jobID := "dt_uncertain_schedule"
	request := actions.PowerRequest{
		JobID: jobID, Action: "shutdown", Delay: 2 * time.Minute,
		Comment: actions.ClassicPowerComment(jobID), RequestedAt: now,
	}
	response := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "schedule", Request: &request})
	if response.OK || response.ErrorCode != "schedule_failed" {
		t.Fatalf("schedule response = %#v", response)
	}
	active, found, err := s.loadActive()
	if err != nil || !found || active.Phase != "schedule_unverified" || active.Receipt.JobID != jobID {
		t.Fatalf("recovery state = %#v, found=%t, err=%v", active, found, err)
	}
}

func TestHelperCanCancelUncertainScheduleWhenUnitsAreConfirmedInactive(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: []runnerResponse{
		{exitCode: 1, err: errors.New("injected scheduling disconnect")},
		{exitCode: 5, err: errors.New("units were not loaded")},
		{exitCode: 4, output: "unknown\n", err: errors.New("unknown unit")},
		{exitCode: 4, output: "unknown\n", err: errors.New("unknown unit")},
		{},
	}}
	config := withDefaults(Config{StateDirectory: root, Runner: runner, HostCheck: func() error { return nil }})
	s := &server{config: config}
	now := time.Now().UTC()
	jobID := "dt_uncertain_cancel"
	request := actions.PowerRequest{
		JobID: jobID, Action: "shutdown", Delay: 2 * time.Minute,
		Comment: actions.ClassicPowerComment(jobID), RequestedAt: now,
	}
	response := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "schedule", Request: &request})
	if response.OK {
		t.Fatalf("uncertain schedule unexpectedly succeeded: %#v", response)
	}
	active, found, err := s.loadActive()
	if err != nil || !found {
		t.Fatalf("active recovery state = %#v, found=%t, err=%v", active, found, err)
	}
	cancel := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "cancel", Receipt: &active.Receipt})
	if !cancel.OK || cancel.CancelResult == nil || !cancel.CancelResult.Cancelled {
		t.Fatalf("cancel response = %#v", cancel)
	}
	if _, err := os.Lstat(s.activePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uncertain state remains after confirmed inactive units: %v", err)
	}
}

func TestTimerCallbackChecksDeadlineAndRejectsExcessiveLateness(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{}
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	config := withDefaults(Config{
		StateDirectory: root, Now: func() time.Time { return now }, Runner: runner,
		HostCheck: func() error { return nil },
	})
	s := &server{config: config}
	jobID := "dt_timer_lateness"
	request := actions.PowerRequest{
		JobID: jobID, Action: "shutdown", Delay: 2 * time.Minute,
		Comment: actions.ClassicPowerComment(jobID), RequestedAt: now,
	}
	response := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "schedule", Request: &request})
	if !response.OK || response.Receipt == nil {
		t.Fatalf("schedule response = %#v", response)
	}
	callsBeforeFire := len(runner.calls)
	now = response.Receipt.Deadline.Add(config.MaxFireLateness + time.Second)
	if err := firePrepared(context.Background(), config, jobID); err == nil || !strings.Contains(err.Error(), "maximum lateness") {
		t.Fatalf("late fire error = %v", err)
	}
	if len(runner.calls) != callsBeforeFire {
		t.Fatalf("late timer invoked power command: %#v", runner.calls[callsBeforeFire:])
	}
	active, found, err := s.loadActive()
	if err != nil || !found || active.Phase != "expired" {
		t.Fatalf("late timer state = %#v, found=%t, err=%v", active, found, err)
	}
}

func TestTimerCallbackUsesOnlyFixedPoweroffCommand(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{}
	now := time.Date(2026, 7, 31, 1, 2, 3, 0, time.UTC)
	config := withDefaults(Config{
		StateDirectory: root, Now: func() time.Time { return now }, Runner: runner,
		HostCheck: func() error { return nil },
	})
	s := &server{config: config}
	jobID := "dt_timer_fire"
	request := actions.PowerRequest{
		JobID: jobID, Action: "shutdown", Delay: 2 * time.Minute,
		Comment: actions.AfterStopPowerComment(jobID), RequestedAt: now,
	}
	response := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "schedule", Request: &request})
	if !response.OK || response.Receipt == nil {
		t.Fatalf("schedule response = %#v", response)
	}
	now = response.Receipt.Deadline.Add(time.Second)
	if err := firePrepared(context.Background(), config, jobID); err != nil {
		t.Fatal(err)
	}
	last := runner.calls[len(runner.calls)-1]
	if strings.Join(last, " ") != "/usr/bin/systemctl poweroff" {
		t.Fatalf("power callback = %#v", last)
	}
}

func TestHelperRejectsModelControlledCommentBeforeExternalCall(t *testing.T) {
	runner := &recordingRunner{}
	config := withDefaults(Config{
		StateDirectory: t.TempDir(), Runner: runner, HostCheck: func() error { return nil },
	})
	s := &server{config: config}
	request := actions.PowerRequest{
		JobID: "dt_bad_comment", Action: "shutdown", Delay: time.Minute,
		Comment: "please run something else", RequestedAt: time.Now().UTC(),
	}
	response := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "schedule", Request: &request})
	if response.OK || response.ErrorCode != "policy_rejected" || len(runner.calls) != 0 {
		t.Fatalf("unsafe request response=%#v calls=%#v", response, runner.calls)
	}
}

func TestHelperRejectsReceiptFromAnotherPeer(t *testing.T) {
	root := t.TempDir()
	runner := &recordingRunner{}
	config := withDefaults(Config{StateDirectory: root, Runner: runner, HostCheck: func() error { return nil }})
	s := &server{config: config}
	now := time.Now().UTC()
	receipt := actions.SealReceipt(actions.Receipt{
		Platform: "linux-systemd", BackendID: "linux-systemd-helper", BackendVersion: "1",
		JobID: "dt_owned", Action: "shutdown", RequestedAt: now, ScheduledAt: now,
		Deadline: now.Add(time.Minute), ExternalToken: actions.SystemdUnitToken("dt_owned"),
		CancelScope: actions.CancelScopeJob,
	})
	if err := s.saveActive(activeState{SchemaVersion: 1, OwnerUID: 1000, Phase: "scheduled", Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	response := s.process(context.Background(), 1001, daemonRequest{SchemaVersion: 1, Operation: "cancel", Receipt: &receipt})
	if response.OK || response.ErrorCode != "peer_rejected" || len(runner.calls) != 0 {
		t.Fatalf("cross-peer cancel response=%#v calls=%#v", response, runner.calls)
	}
}

func TestReconcileReleasesInactiveHelperStateWithoutRetryingPower(t *testing.T) {
	root := t.TempDir()
	runner := &scriptedRunner{responses: []runnerResponse{
		{exitCode: 4, output: "unknown\n", err: errors.New("unknown timer")},
		{exitCode: 4, output: "unknown\n", err: errors.New("unknown service")},
	}}
	now := time.Now().UTC()
	config := withDefaults(Config{StateDirectory: root, Now: func() time.Time { return now }, Runner: runner, HostCheck: func() error { return nil }})
	s := &server{config: config}
	receipt := actions.SealReceipt(actions.Receipt{
		Platform: "linux-systemd", BackendID: "linux-systemd-helper", BackendVersion: "1",
		JobID: "dt_reconcile_cleanup", Action: "shutdown", RequestedAt: now.Add(-3 * time.Minute), ScheduledAt: now.Add(-3 * time.Minute),
		Deadline: now.Add(-time.Minute), ExternalToken: actions.SystemdUnitToken("dt_reconcile_cleanup"),
		CancelScope: actions.CancelScopeJob,
	})
	if err := s.saveActive(activeState{SchemaVersion: 1, OwnerUID: 1000, Phase: "fire_unverified", Receipt: receipt}); err != nil {
		t.Fatal(err)
	}
	response := s.process(context.Background(), 1000, daemonRequest{SchemaVersion: 1, Operation: "reconcile", Receipt: &receipt})
	if !response.OK || response.Reconcile == nil || response.Reconcile.State != actions.ReconcileUnverified {
		t.Fatalf("reconcile response = %#v", response)
	}
	if _, err := os.Lstat(s.activePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inactive helper state was not released: %v", err)
	}
	for _, call := range runner.calls {
		if len(call) > 1 && call[1] == "poweroff" {
			t.Fatalf("reconcile retried a power action: %#v", runner.calls)
		}
	}
}
