//go:build windows

package platform

import (
	"fmt"
	"syscall"
	"unsafe"
)

const errorAlreadyExists syscall.Errno = 183

var (
	kernel32Platform = syscall.NewLazyDLL("kernel32.dll")
	createMutexProc  = kernel32Platform.NewProc("CreateMutexW")
	closeHandleProc  = kernel32Platform.NewProc("CloseHandle")
)

type namedMutex struct {
	handle syscall.Handle
}

func AcquirePowerLock() (PowerLock, error) {
	// Shutdown is machine-wide, so the guard must span Windows sessions too.
	name, err := syscall.UTF16PtrFromString(`Global\DoneThen-PowerAction`)
	if err != nil {
		return nil, fmt.Errorf("encode mutex name: %w", err)
	}
	handle, _, callErr := createMutexProc.Call(0, 0, uintptr(unsafe.Pointer(name)))
	if handle == 0 {
		return nil, fmt.Errorf("CreateMutexW: %w", callErr)
	}
	if errno, ok := callErr.(syscall.Errno); ok && errno == errorAlreadyExists {
		_, _, _ = closeHandleProc.Call(handle)
		return nil, ErrPowerLockHeld
	}
	return &namedMutex{handle: syscall.Handle(handle)}, nil
}

func (m *namedMutex) Release() error {
	if m.handle == 0 {
		return nil
	}
	result, _, callErr := closeHandleProc.Call(uintptr(m.handle))
	m.handle = 0
	if result == 0 {
		return fmt.Errorf("CloseHandle: %w", callErr)
	}
	return nil
}
