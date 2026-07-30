//go:build !windows

package pluginstate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type fileStateLock struct {
	file *os.File
}

func acquireStateLock(root string, timeout time.Duration) (stateLock, error) {
	file, err := os.OpenFile(filepath.Join(root, ".state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open plugin state lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return &fileStateLock{file: file}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, fmt.Errorf("lock plugin state: %w", err)
		}
		if !time.Now().Before(deadline) {
			_ = file.Close()
			return nil, ErrLockTimeout
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (l *fileStateLock) Release() error {
	if l.file == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		return fmt.Errorf("unlock plugin state: %w", unlockErr)
	}
	return closeErr
}
