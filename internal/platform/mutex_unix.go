//go:build linux || darwin

package platform

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
)

type unixPowerLock struct {
	file *os.File
}

func AcquirePowerLock() (PowerLock, error) {
	path := "/run/donethen/power.lock"
	if runtime.GOOS == "darwin" {
		path = "/var/run/donethen/power.lock"
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect machine power lock: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o007 != 0 {
		return nil, errors.New("machine power lock must be a root-owned regular file that is not writable by other users")
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open machine power lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrPowerLockHeld
		}
		return nil, fmt.Errorf("lock machine power coordinator: %w", err)
	}
	return &unixPowerLock{file: file}, nil
}

func (l *unixPowerLock) Release() error {
	if l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return fmt.Errorf("unlock machine power coordinator: %w", unlockErr)
	}
	return closeErr
}
