package actions

import (
	"context"
	"errors"
	"time"
)

var ErrNoShutdownInProgress = errors.New("no shutdown is in progress")

type Backend interface {
	ScheduleShutdown(ctx context.Context, delay time.Duration, comment string) error
	AbortShutdown(ctx context.Context) error
}

type ProcessRunner interface {
	Run(ctx context.Context, executable string, args ...string) (exitCode int, output []byte, err error)
}
