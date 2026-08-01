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
	"strings"
	"syscall"
	"time"
	"unsafe"
)

type WindowsBackend struct {
	runner             ProcessRunner
	now                func() time.Time
	bootID             func() string
	shutdownExecutable func() (string, error)
	environmentCheck   func() error
}

func NewPlatformBackend() Backend {
	return WindowsBackend{
		runner: execProcessRunner{}, now: time.Now, bootID: platformBootID,
		shutdownExecutable: systemShutdownExecutable,
		environmentCheck:   validateWindowsExecutionEnvironment,
	}
}

func NewWindowsBackendWithRunner(runner ProcessRunner) Backend {
	return WindowsBackend{runner: runner, now: time.Now, bootID: platformBootID, shutdownExecutable: systemShutdownExecutable}
}

func (b WindowsBackend) Preflight(_ context.Context, request PowerRequest) (Capabilities, error) {
	capabilities := Capabilities{
		Platform:           "windows",
		BackendID:          "windows-shutdown-exe",
		ExecuteSupported:   true,
		CancelScope:        CancelScopeSystemGlobal,
		MinimumDelay:       30 * time.Second,
		MaximumDelay:       time.Hour,
		ReconcileSupported: true,
	}
	if err := ValidateRequest(request, capabilities.MinimumDelay, capabilities.MaximumDelay); err != nil {
		return capabilities, err
	}
	if b.runner == nil {
		return capabilities, errors.New("Windows action process runner is not configured")
	}
	if b.environmentCheck != nil {
		if err := b.environmentCheck(); err != nil {
			capabilities.ExecuteSupported = false
			capabilities.Reason = err.Error()
			return capabilities, err
		}
	}
	if b.shutdownExecutable == nil {
		return capabilities, errors.New("Windows shutdown executable resolver is not configured")
	}
	if _, err := b.shutdownExecutable(); err != nil {
		capabilities.ExecuteSupported = false
		capabilities.Reason = err.Error()
		return capabilities, err
	}
	return capabilities, nil
}

func (b WindowsBackend) Schedule(ctx context.Context, request PowerRequest) (Receipt, error) {
	capabilities, err := b.Preflight(ctx, request)
	if err != nil {
		return Receipt{}, err
	}
	executable, err := b.shutdownExecutable()
	if err != nil {
		return Receipt{}, err
	}
	seconds := int64(request.Delay / time.Second)
	exitCode, output, err := b.runner.Run(ctx, executable, "/s", "/t", strconv.FormatInt(seconds, 10), "/c", request.Comment)
	if err != nil || exitCode != 0 {
		if err == nil {
			err = fmt.Errorf("shutdown.exe exited with code %d", exitCode)
		}
		return Receipt{}, fmt.Errorf("schedule Windows shutdown: %w (%s)", err, cleanOutput(output))
	}
	now := b.currentTime()
	return SealReceipt(Receipt{
		SchemaVersion:  ReceiptSchemaVersion,
		Platform:       capabilities.Platform,
		BackendID:      capabilities.BackendID,
		BackendVersion: "1",
		JobID:          request.JobID,
		Action:         request.Action,
		RequestedAt:    request.RequestedAt.UTC(),
		ScheduledAt:    now,
		Deadline:       now.Add(request.Delay),
		CancelScope:    capabilities.CancelScope,
		BootID:         b.currentBootID(),
		ResultCode:     exitCode,
		ResultSummary:  "Windows accepted shutdown.exe countdown",
	}), nil
}

func (b WindowsBackend) Cancel(ctx context.Context, receipt Receipt) (CancelResult, error) {
	if err := validateWindowsReceipt(receipt); err != nil {
		return CancelResult{}, err
	}
	if b.runner == nil {
		return CancelResult{}, errors.New("Windows action process runner is not configured")
	}
	if b.shutdownExecutable == nil {
		return CancelResult{}, errors.New("Windows shutdown executable resolver is not configured")
	}
	executable, err := b.shutdownExecutable()
	if err != nil {
		return CancelResult{}, err
	}
	exitCode, output, err := b.runner.Run(ctx, executable, "/a")
	if err != nil || exitCode != 0 {
		if exitCode == 1116 {
			return CancelResult{
				NoActionInProgress: true,
				Scope:              CancelScopeSystemGlobal,
				ResultCode:         exitCode,
				ResultSummary:      "Windows reported no shutdown in progress",
			}, fmt.Errorf("%w: %s", ErrNoShutdownInProgress, cleanOutput(output))
		}
		if err == nil {
			err = fmt.Errorf("shutdown.exe exited with code %d", exitCode)
		}
		return CancelResult{Scope: CancelScopeSystemGlobal, ResultCode: exitCode}, fmt.Errorf("abort Windows shutdown: %w (%s)", err, cleanOutput(output))
	}
	return CancelResult{
		Cancelled:     true,
		Scope:         CancelScopeSystemGlobal,
		ResultCode:    exitCode,
		ResultSummary: "Windows accepted shutdown.exe /a",
	}, nil
}

func (b WindowsBackend) Reconcile(_ context.Context, receipt Receipt) (ReconcileResult, error) {
	if err := validateWindowsReceipt(receipt); err != nil {
		return ReconcileResult{}, err
	}
	now := b.currentTime()
	currentBootID := b.currentBootID()
	result := ReconcileResult{CheckedAt: now, CurrentBootID: currentBootID}
	if now.Before(receipt.Deadline) && (receipt.BootID == "" || receipt.BootID == currentBootID) {
		result.State = ReconcileScheduled
		result.Evidence = "countdown deadline has not elapsed on the recorded boot"
		return result, nil
	}
	result.State = ReconcileUnverified
	if receipt.BootID != "" && receipt.BootID != currentBootID {
		result.Evidence = "Windows boot identity changed after scheduling; causation is not independently proven"
	} else {
		result.Evidence = "countdown deadline elapsed without sufficient evidence to confirm poweroff"
	}
	return result, nil
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

var getSystemDirectoryProc = syscall.NewLazyDLL("kernel32.dll").NewProc("GetSystemDirectoryW")

func systemShutdownExecutable() (string, error) {
	buffer := make([]uint16, 260)
	for {
		length, _, callErr := getSystemDirectoryProc.Call(uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		if length == 0 {
			return "", fmt.Errorf("resolve Windows system directory: %w", callErr)
		}
		if length < uintptr(len(buffer)) {
			directory := syscall.UTF16ToString(buffer[:length])
			path := filepath.Join(directory, "shutdown.exe")
			info, err := os.Lstat(path)
			if err != nil {
				return "", fmt.Errorf("inspect Windows shutdown executable: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return "", errors.New("Windows shutdown executable is a link or special file")
			}
			return path, nil
		}
		if length > 32767 {
			return "", errors.New("Windows system directory path is unexpectedly long")
		}
		buffer = make([]uint16, int(length)+1)
	}
}

func validateWindowsExecutionEnvironment() error {
	for _, key := range []string{"CI", "GITHUB_ACTIONS", "TF_BUILD"} {
		value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
		if value == "1" || value == "true" || value == "yes" {
			return fmt.Errorf("%w: Windows power actions are disabled in CI environments", ErrPlatformUnsupported)
		}
	}
	return nil
}

func cleanOutput(output []byte) string {
	const max = 512
	if len(output) > max {
		output = output[:max]
	}
	return string(output)
}

func validateWindowsReceipt(receipt Receipt) error {
	if err := ValidateReceipt(receipt); err != nil {
		return err
	}
	if receipt.Platform != "windows" || receipt.BackendID != "windows-shutdown-exe" {
		return errors.New("power receipt does not belong to the Windows shutdown backend")
	}
	return nil
}

func (b WindowsBackend) currentTime() time.Time {
	if b.now == nil {
		return time.Now().UTC()
	}
	return b.now().UTC()
}

func (b WindowsBackend) currentBootID() string {
	if b.bootID == nil {
		return ""
	}
	return b.bootID()
}
