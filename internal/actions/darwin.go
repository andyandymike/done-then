//go:build darwin

package actions

import (
	"context"
	"time"
)

type darwinBackend struct{}

func NewPlatformBackend() Backend { return darwinBackend{} }

func (darwinBackend) Preflight(_ context.Context, request PowerRequest) (Capabilities, error) {
	capabilities := Capabilities{
		Platform:           "darwin",
		BackendID:          "macos-signed-helper",
		ExecuteSupported:   false,
		CancelScope:        CancelScopeJob,
		MinimumDelay:       30 * time.Second,
		MaximumDelay:       time.Hour,
		ReconcileSupported: false,
		Reason:             "the signed and notarized macOS helper is not included in this source build",
	}
	if err := ValidateRequest(request, capabilities.MinimumDelay, capabilities.MaximumDelay); err != nil {
		return capabilities, err
	}
	return capabilities, ErrPlatformUnsupported
}

func (darwinBackend) Schedule(context.Context, PowerRequest) (Receipt, error) {
	return Receipt{}, ErrPlatformUnsupported
}

func (darwinBackend) Cancel(context.Context, Receipt) (CancelResult, error) {
	return CancelResult{}, ErrPlatformUnsupported
}

func (darwinBackend) Reconcile(context.Context, Receipt) (ReconcileResult, error) {
	return ReconcileResult{}, ErrPlatformUnsupported
}
