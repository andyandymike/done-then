//go:build !windows

package platform

import "errors"

func AcquirePowerLock() (PowerLock, error) {
	return nil, errors.New("DoneThen v0.1 power locking is only implemented on Windows")
}
