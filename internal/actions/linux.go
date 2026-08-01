//go:build linux

package actions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

const linuxHelperSocket = "/run/donethen/powerd.sock"

type LinuxBackend struct {
	client   powerHelperClient
	platform func() error
}

func NewPlatformBackend() Backend {
	return LinuxBackend{
		client:   unixHelperClient{socketPath: linuxHelperSocket, timeout: 5 * time.Second},
		platform: validateLinuxHost,
	}
}

func (b LinuxBackend) Preflight(_ context.Context, request PowerRequest) (Capabilities, error) {
	capabilities := linuxCapabilities()
	if err := ValidateRequest(request, capabilities.MinimumDelay, capabilities.MaximumDelay); err != nil {
		return capabilities, err
	}
	if b.platform == nil || b.client == nil {
		return capabilities, errors.New("Linux helper backend is not configured")
	}
	if err := b.platform(); err != nil {
		capabilities.Reason = err.Error()
		return capabilities, err
	}
	response, err := b.client.Call(helperPreflight, &request, nil)
	if err != nil {
		capabilities.Reason = "the installed Linux power helper did not pass preflight"
		return capabilities, err
	}
	if response.Capabilities == nil || response.Capabilities.Platform != capabilities.Platform ||
		response.Capabilities.BackendID != capabilities.BackendID || !response.Capabilities.ExecuteSupported {
		return capabilities, errors.New("Linux power helper returned incompatible capabilities")
	}
	return *response.Capabilities, nil
}

func (b LinuxBackend) Schedule(ctx context.Context, request PowerRequest) (Receipt, error) {
	capabilities, err := b.Preflight(ctx, request)
	if err != nil {
		return Receipt{}, err
	}
	response, err := b.client.Call(helperSchedule, &request, nil)
	if err != nil {
		return Receipt{}, err
	}
	if response.Receipt == nil {
		return Receipt{}, errors.New("Linux power helper returned no receipt")
	}
	if err := validateLinuxReceipt(*response.Receipt); err != nil {
		return Receipt{}, err
	}
	if err := ValidateReceiptForRequest(*response.Receipt, request, capabilities); err != nil {
		return Receipt{}, fmt.Errorf("validate Linux power helper receipt: %w", err)
	}
	return *response.Receipt, nil
}

func (b LinuxBackend) Cancel(_ context.Context, receipt Receipt) (CancelResult, error) {
	if err := validateLinuxReceipt(receipt); err != nil {
		return CancelResult{}, err
	}
	response, err := b.client.Call(helperCancel, nil, &receipt)
	if err != nil {
		if errors.Is(err, ErrNoShutdownInProgress) && response.CancelResult != nil {
			return *response.CancelResult, err
		}
		return CancelResult{}, err
	}
	if response.CancelResult == nil || response.CancelResult.Scope != CancelScopeJob {
		return CancelResult{}, errors.New("Linux power helper returned an invalid cancellation result")
	}
	return *response.CancelResult, nil
}

func (b LinuxBackend) Reconcile(_ context.Context, receipt Receipt) (ReconcileResult, error) {
	if err := validateLinuxReceipt(receipt); err != nil {
		return ReconcileResult{}, err
	}
	response, err := b.client.Call(helperReconcile, nil, &receipt)
	if err != nil {
		return ReconcileResult{}, err
	}
	if response.Reconcile == nil {
		return ReconcileResult{}, errors.New("Linux power helper returned no reconciliation result")
	}
	return *response.Reconcile, nil
}

func linuxCapabilities() Capabilities {
	return Capabilities{
		Platform:           "linux-systemd",
		BackendID:          "linux-systemd-helper",
		ExecuteSupported:   false,
		CancelScope:        CancelScopeJob,
		MinimumDelay:       30 * time.Second,
		MaximumDelay:       time.Hour,
		ReconcileSupported: true,
		Reason:             "systemd power helper preflight has not completed",
	}
}

func validateLinuxReceipt(receipt Receipt) error {
	if err := ValidateReceipt(receipt); err != nil {
		return err
	}
	if receipt.Platform != "linux-systemd" || receipt.BackendID != "linux-systemd-helper" || receipt.CancelScope != CancelScopeJob {
		return errors.New("power receipt does not belong to the Linux systemd helper")
	}
	if strings.TrimSpace(receipt.ExternalToken) == "" {
		return errors.New("Linux power receipt is missing its systemd unit token")
	}
	return nil
}

func validateLinuxHost() error {
	comm, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(comm)) != "systemd" {
		return fmt.Errorf("%w: PID 1 is not systemd", ErrPlatformUnsupported)
	}
	if release, readErr := os.ReadFile("/proc/sys/kernel/osrelease"); readErr == nil && strings.Contains(strings.ToLower(string(release)), "microsoft") {
		return fmt.Errorf("%w: WSL is outside the Linux power profile", ErrPlatformUnsupported)
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, statErr := os.Stat(marker); statErr == nil {
			return fmt.Errorf("%w: containers are outside the Linux power profile", ErrPlatformUnsupported)
		}
	}
	info, err := os.Lstat(linuxHelperSocket)
	if err != nil {
		return fmt.Errorf("%w: Linux power helper socket is unavailable", ErrPrivilegeUnavailable)
	}
	if info.Mode()&os.ModeSymlink != 0 || info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%w: Linux power helper endpoint is not a Unix socket", ErrPrivilegeUnavailable)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode().Perm()&0o002 != 0 {
		return fmt.Errorf("%w: Linux power helper socket ownership or mode is unsafe", ErrPrivilegeUnavailable)
	}
	return nil
}
