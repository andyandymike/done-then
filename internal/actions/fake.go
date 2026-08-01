package actions

import (
	"context"
	"sync"
	"time"
)

type FakeBackend struct {
	mu sync.Mutex

	PreflightCalls  int
	ScheduleCalls   int
	CancelCalls     int
	ReconcileCalls  int
	LastDelay       time.Duration
	LastComment     string
	LastRequest     PowerRequest
	LastReceipt     Receipt
	Capabilities    Capabilities
	Receipt         Receipt
	CancelResult    CancelResult
	ReconcileResult ReconcileResult
	PreflightErr    error
	ScheduleErr     error
	CancelErr       error
	ReconcileErr    error
}

func (f *FakeBackend) Preflight(_ context.Context, request PowerRequest) (Capabilities, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PreflightCalls++
	f.LastRequest = request
	capabilities := f.Capabilities
	if capabilities.Platform == "" {
		capabilities = Capabilities{
			Platform:           "fake",
			BackendID:          "fake",
			ExecuteSupported:   true,
			CancelScope:        CancelScopeJob,
			MinimumDelay:       30 * time.Second,
			MaximumDelay:       time.Hour,
			ReconcileSupported: true,
		}
	}
	return capabilities, f.PreflightErr
}

func (f *FakeBackend) Schedule(_ context.Context, request PowerRequest) (Receipt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ScheduleCalls++
	f.LastRequest = request
	f.LastDelay = request.Delay
	f.LastComment = request.Comment
	receipt := f.Receipt
	if receipt.SchemaVersion == "" {
		receipt = SealReceipt(Receipt{
			SchemaVersion:  ReceiptSchemaVersion,
			Platform:       "fake",
			BackendID:      "fake",
			BackendVersion: "1",
			JobID:          request.JobID,
			Action:         request.Action,
			RequestedAt:    request.RequestedAt,
			ScheduledAt:    request.RequestedAt,
			Deadline:       request.RequestedAt.Add(request.Delay),
			ExternalToken:  request.JobID,
			CancelScope:    CancelScopeJob,
		})
	} else if receipt.SchemaVersion == ReceiptSchemaVersion && receipt.Checksum == "" {
		receipt = SealReceipt(receipt)
	}
	f.LastReceipt = receipt
	return receipt, f.ScheduleErr
}

func (f *FakeBackend) Cancel(_ context.Context, receipt Receipt) (CancelResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.CancelCalls++
	f.LastReceipt = receipt
	result := f.CancelResult
	if result.Scope == "" {
		result = CancelResult{Cancelled: true, Scope: receipt.CancelScope}
	}
	return result, f.CancelErr
}

func (f *FakeBackend) Reconcile(_ context.Context, receipt Receipt) (ReconcileResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ReconcileCalls++
	f.LastReceipt = receipt
	result := f.ReconcileResult
	if result.State == "" {
		result = ReconcileResult{State: ReconcileScheduled, CheckedAt: time.Now().UTC()}
	}
	return result, f.ReconcileErr
}

func (f *FakeBackend) Snapshot() (scheduleCalls, cancelCalls int, delay time.Duration, comment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ScheduleCalls, f.CancelCalls, f.LastDelay, f.LastComment
}
