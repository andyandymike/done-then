//go:build !windows

package actions

import (
	"context"
	"errors"
	"time"
)

type unsupportedBackend struct{}

func NewPlatformBackend() Backend {
	return unsupportedBackend{}
}

func (unsupportedBackend) ScheduleShutdown(context.Context, time.Duration, string) error {
	return errors.New("DoneThen v0.1 only supports Windows shutdown")
}

func (unsupportedBackend) AbortShutdown(context.Context) error {
	return errors.New("DoneThen v0.1 only supports Windows shutdown cancellation")
}
