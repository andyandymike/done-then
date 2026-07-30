//go:build windows

package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

type WindowsBackend struct {
	runner ProcessRunner
}

func NewPlatformBackend() Backend {
	return WindowsBackend{runner: execProcessRunner{}}
}

func NewWindowsBackendWithRunner(runner ProcessRunner) Backend {
	return WindowsBackend{runner: runner}
}

func (b WindowsBackend) ScheduleShutdown(ctx context.Context, delay time.Duration, comment string) error {
	seconds := int64(delay / time.Second)
	if seconds < 30 || seconds > int64(time.Hour/time.Second) {
		return fmt.Errorf("shutdown delay must be between 30s and 1h")
	}
	if b.runner == nil {
		return errors.New("Windows action process runner is not configured")
	}
	_, output, err := b.runner.Run(ctx, shutdownPath(), "/s", "/t", strconv.FormatInt(seconds, 10), "/c", comment)
	if err != nil {
		return fmt.Errorf("schedule Windows shutdown: %w (%s)", err, cleanOutput(output))
	}
	return nil
}

func (b WindowsBackend) AbortShutdown(ctx context.Context) error {
	if b.runner == nil {
		return errors.New("Windows action process runner is not configured")
	}
	exitCode, output, err := b.runner.Run(ctx, shutdownPath(), "/a")
	if err != nil {
		if exitCode == 1116 {
			return fmt.Errorf("%w: %s", ErrNoShutdownInProgress, cleanOutput(output))
		}
		return fmt.Errorf("abort Windows shutdown: %w (%s)", err, cleanOutput(output))
	}
	return nil
}

type execProcessRunner struct{}

func (execProcessRunner) Run(ctx context.Context, executable string, args ...string) (int, []byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return 0, output, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode(), output, err
	}
	return -1, output, err
}

func shutdownPath() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "shutdown.exe")
}

func cleanOutput(output []byte) string {
	const max = 512
	if len(output) > max {
		output = output[:max]
	}
	return string(output)
}
