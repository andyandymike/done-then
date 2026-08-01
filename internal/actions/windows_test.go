//go:build windows

package actions

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type capturedProcess struct {
	executable string
	args       []string
	exitCode   int
	output     []byte
	err        error
	calls      int
}

func (p *capturedProcess) Run(_ context.Context, executable string, args ...string) (int, []byte, error) {
	p.calls++
	p.executable = executable
	p.args = append([]string(nil), args...)
	return p.exitCode, p.output, p.err
}

func TestWindowsBackendSchedulesNarrowShutdownCommand(t *testing.T) {
	runner := &capturedProcess{}
	backend := NewWindowsBackendWithRunner(runner)
	request := testPowerRequest(2 * time.Minute)
	receipt, err := backend.Schedule(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(filepath.Base(runner.executable)) != "shutdown.exe" {
		t.Fatalf("executable = %q", runner.executable)
	}
	want := []string{"/s", "/t", "120", "/c", "DoneThen job dt_TEST completed"}
	if !reflect.DeepEqual(runner.args, want) {
		t.Fatalf("args = %#v, want %#v", runner.args, want)
	}
	for _, arg := range runner.args {
		if arg == "/f" {
			t.Fatal("shutdown command contains /f")
		}
	}
	if receipt.JobID != request.JobID || receipt.CancelScope != CancelScopeSystemGlobal || receipt.BackendID != "windows-shutdown-exe" {
		t.Fatalf("receipt = %#v", receipt)
	}
	if !receipt.Deadline.Equal(receipt.ScheduledAt.Add(request.Delay)) {
		t.Fatalf("receipt deadline = %s", receipt.Deadline)
	}
}

func TestWindowsBackendRejectsInvalidDelayBeforeProcessCall(t *testing.T) {
	runner := &capturedProcess{}
	backend := NewWindowsBackendWithRunner(runner)
	if _, err := backend.Schedule(context.Background(), testPowerRequest(29*time.Second)); err == nil {
		t.Fatal("Schedule() accepted an unsafe delay")
	}
	if runner.calls != 0 {
		t.Fatalf("process calls = %d", runner.calls)
	}
}

func TestWindowsBackendRejectsNonzeroExitWithoutRunnerError(t *testing.T) {
	runner := &capturedProcess{exitCode: 5}
	backend := NewWindowsBackendWithRunner(runner)
	if _, err := backend.Schedule(context.Background(), testPowerRequest(2*time.Minute)); err == nil {
		t.Fatal("Schedule() accepted a nonzero process exit code")
	}
}

func TestWindowsBackendRecognizesNoPendingShutdown(t *testing.T) {
	runner := &capturedProcess{exitCode: 1116, err: errors.New("exit status 1116")}
	backend := NewWindowsBackendWithRunner(runner)
	request := testPowerRequest(2 * time.Minute)
	receipt := SealReceipt(Receipt{
		SchemaVersion:  ReceiptSchemaVersion,
		Platform:       "windows",
		BackendID:      "windows-shutdown-exe",
		BackendVersion: "1",
		JobID:          request.JobID,
		Action:         request.Action,
		RequestedAt:    request.RequestedAt,
		ScheduledAt:    request.RequestedAt,
		Deadline:       request.RequestedAt.Add(request.Delay),
		CancelScope:    CancelScopeSystemGlobal,
	})
	result, err := backend.Cancel(context.Background(), receipt)
	if !errors.Is(err, ErrNoShutdownInProgress) {
		t.Fatalf("Cancel() error = %v", err)
	}
	if !result.NoActionInProgress || result.Scope != CancelScopeSystemGlobal {
		t.Fatalf("Cancel() result = %#v", result)
	}
	if !reflect.DeepEqual(runner.args, []string{"/a"}) {
		t.Fatalf("abort args = %#v", runner.args)
	}
}

func testPowerRequest(delay time.Duration) PowerRequest {
	return PowerRequest{
		JobID:       "dt_TEST",
		Action:      "shutdown",
		Delay:       delay,
		Comment:     "DoneThen job dt_TEST completed",
		RequestedAt: time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
	}
}
