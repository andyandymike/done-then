package actions

import (
	"testing"
	"time"
)

func TestIntentReceiptIsCancellableButNotScheduleProof(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	capabilities := Capabilities{
		Platform: "linux-systemd", BackendID: "linux-systemd-helper", ExecuteSupported: true,
		CancelScope: CancelScopeJob, MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour,
	}
	receipt, err := BuildIntentReceipt("dt_RECOVERY123", "shutdown", now, 2*time.Minute, capabilities)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateReceipt(receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.ResultCode != -1 || receipt.ExternalToken != SystemdUnitToken(receipt.JobID) ||
		receipt.ResultSummary != "action intent persisted before external scheduling" {
		t.Fatalf("intent receipt = %#v", receipt)
	}
	tampered := receipt
	tampered.Deadline = tampered.Deadline.Add(time.Second)
	if err := ValidateReceipt(tampered); err == nil {
		t.Fatal("tampered recovery receipt passed checksum validation")
	}
}

func TestIntentReceiptRejectsUnsafeCapabilityOrDelay(t *testing.T) {
	now := time.Now().UTC()
	capabilities := Capabilities{
		Platform: "fake", BackendID: "fake", CancelScope: CancelScopeJob,
		MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour,
	}
	for _, test := range []struct {
		name  string
		delay time.Duration
		caps  Capabilities
	}{
		{name: "below minimum", delay: 29 * time.Second, caps: capabilities},
		{name: "fractional", delay: 30*time.Second + time.Millisecond, caps: capabilities},
		{name: "missing backend", delay: 30 * time.Second, caps: Capabilities{CancelScope: CancelScopeJob}},
		{name: "invalid cancel scope", delay: 30 * time.Second, caps: Capabilities{Platform: "fake", BackendID: "fake"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := BuildIntentReceipt("dt_RECOVERY123", "shutdown", now, test.delay, test.caps); err == nil {
				t.Fatal("unsafe intent receipt unexpectedly succeeded")
			}
		})
	}
}

func TestScheduledReceiptMustMatchRequestCapabilitiesAndDelay(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	capabilities := Capabilities{
		Platform: "fake", BackendID: "fake", ExecuteSupported: true,
		CancelScope: CancelScopeJob, MinimumDelay: 30 * time.Second, MaximumDelay: time.Hour,
	}
	request := PowerRequest{
		JobID: "dt_RECEIPT123", Action: "shutdown", Delay: 2 * time.Minute,
		Comment: "DoneThen job dt_RECEIPT123 completed", RequestedAt: now,
	}
	receipt := SealReceipt(Receipt{
		Platform: "fake", BackendID: "fake", BackendVersion: "1", JobID: request.JobID,
		Action: request.Action, RequestedAt: now, ScheduledAt: now.Add(time.Second),
		Deadline: now.Add(time.Second).Add(request.Delay), ExternalToken: "fixed-token",
		CancelScope: CancelScopeJob, ResultCode: 0,
	})
	if err := ValidateReceiptForRequest(receipt, request, capabilities); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*Receipt){
		"backend":  func(value *Receipt) { value.BackendID = "other" },
		"deadline": func(value *Receipt) { value.Deadline = value.Deadline.Add(time.Second) },
		"result":   func(value *Receipt) { value.ResultCode = 1 },
		"token":    func(value *Receipt) { value.ExternalToken = "" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := receipt
			mutate(&changed)
			changed = SealReceipt(changed)
			if err := ValidateReceiptForRequest(changed, request, capabilities); err == nil {
				t.Fatal("mismatched scheduled receipt was accepted")
			}
		})
	}
}
