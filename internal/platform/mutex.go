package platform

import "errors"

var ErrPowerLockHeld = errors.New("another DoneThen power job is active")

type PowerLock interface {
	Release() error
}
