//go:build !windows && !linux && !darwin

package actions

import (
	"context"
	"time"
)

type unsupportedBackend struct{}

func NewPlatformBackend() Backend {
	return unsupportedBackend{}
}

func (unsupportedBackend) Preflight(context.Context, PowerRequest) (Capabilities, error) {
	return Capabilities{
		Platform:         "unsupported",
		BackendID:        "unsupported",
		ExecuteSupported: false,
		MinimumDelay:     30 * time.Second,
		MaximumDelay:     time.Hour,
		Reason:           "DoneThen power actions are not implemented on this platform",
	}, ErrPlatformUnsupported
}

func (unsupportedBackend) Schedule(context.Context, PowerRequest) (Receipt, error) {
	return Receipt{}, ErrPlatformUnsupported
}

func (unsupportedBackend) Cancel(context.Context, Receipt) (CancelResult, error) {
	return CancelResult{}, ErrPlatformUnsupported
}

func (unsupportedBackend) Reconcile(context.Context, Receipt) (ReconcileResult, error) {
	return ReconcileResult{}, ErrPlatformUnsupported
}
