package powerdaemon

import (
	"context"
	"errors"
	"time"
)

var ErrUnsupported = errors.New("DoneThen power helper is only supported on Linux systemd hosts")

type Config struct {
	SocketPath      string
	StateDirectory  string
	GroupName       string
	HelperPath      string
	SystemdRunPath  string
	SystemctlPath   string
	MaxFireLateness time.Duration
	Now             func() time.Time
	Runner          Runner
	HostCheck       func() error
}

type Runner interface {
	Run(ctx context.Context, executable string, args ...string) (exitCode int, output []byte, err error)
}
