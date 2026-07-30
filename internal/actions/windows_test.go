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
	if err := backend.ScheduleShutdown(context.Background(), 2*time.Minute, "DoneThen job dt_TEST completed"); err != nil {
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
}

func TestWindowsBackendRejectsInvalidDelayBeforeProcessCall(t *testing.T) {
	runner := &capturedProcess{}
	backend := NewWindowsBackendWithRunner(runner)
	if err := backend.ScheduleShutdown(context.Background(), 29*time.Second, "comment"); err == nil {
		t.Fatal("ScheduleShutdown() accepted an unsafe delay")
	}
	if runner.calls != 0 {
		t.Fatalf("process calls = %d", runner.calls)
	}
}

func TestWindowsBackendRecognizesNoPendingShutdown(t *testing.T) {
	runner := &capturedProcess{exitCode: 1116, err: errors.New("exit status 1116")}
	backend := NewWindowsBackendWithRunner(runner)
	err := backend.AbortShutdown(context.Background())
	if !errors.Is(err, ErrNoShutdownInProgress) {
		t.Fatalf("AbortShutdown() error = %v", err)
	}
	if !reflect.DeepEqual(runner.args, []string{"/a"}) {
		t.Fatalf("abort args = %#v", runner.args)
	}
}
