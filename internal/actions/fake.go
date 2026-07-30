package actions

import (
	"context"
	"sync"
	"time"
)

type FakeBackend struct {
	mu sync.Mutex

	ScheduleCalls int
	AbortCalls    int
	LastDelay     time.Duration
	LastComment   string
	ScheduleErr   error
	AbortErr      error
}

func (f *FakeBackend) ScheduleShutdown(_ context.Context, delay time.Duration, comment string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ScheduleCalls++
	f.LastDelay = delay
	f.LastComment = comment
	return f.ScheduleErr
}

func (f *FakeBackend) AbortShutdown(_ context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.AbortCalls++
	return f.AbortErr
}

func (f *FakeBackend) Snapshot() (scheduleCalls, abortCalls int, delay time.Duration, comment string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ScheduleCalls, f.AbortCalls, f.LastDelay, f.LastComment
}
