//go:build windows

package pluginstate

import (
	"fmt"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"github.com/andyandymike/done-then/internal/identity"
)

const (
	waitObject0   = 0x00000000
	waitAbandoned = 0x00000080
	waitTimeout   = 0x00000102
)

var (
	kernel32PluginState     = syscall.NewLazyDLL("kernel32.dll")
	createMutexPluginProc   = kernel32PluginState.NewProc("CreateMutexW")
	waitForSingleObjectProc = kernel32PluginState.NewProc("WaitForSingleObject")
	releaseMutexPluginProc  = kernel32PluginState.NewProc("ReleaseMutex")
	closeHandlePluginProc   = kernel32PluginState.NewProc("CloseHandle")
)

type windowsStateLock struct {
	handle syscall.Handle
}

func acquireStateLock(root string, timeout time.Duration) (stateLock, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve plugin lock identity: %w", err)
	}
	name := `Local\DoneThen-PluginState-` + identity.SHA256([]byte(filepath.Clean(absolute)))
	namePtr, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("encode plugin mutex name: %w", err)
	}
	handle, _, callErr := createMutexPluginProc.Call(0, 0, uintptr(unsafe.Pointer(namePtr)))
	if handle == 0 {
		return nil, fmt.Errorf("CreateMutexW for plugin state: %w", callErr)
	}
	waitMillis := uint32(timeout / time.Millisecond)
	result, _, waitErr := waitForSingleObjectProc.Call(handle, uintptr(waitMillis))
	switch result {
	case waitObject0, waitAbandoned:
		return &windowsStateLock{handle: syscall.Handle(handle)}, nil
	case waitTimeout:
		_, _, _ = closeHandlePluginProc.Call(handle)
		return nil, ErrLockTimeout
	default:
		_, _, _ = closeHandlePluginProc.Call(handle)
		return nil, fmt.Errorf("WaitForSingleObject for plugin state: %w", waitErr)
	}
}

func (l *windowsStateLock) Release() error {
	if l.handle == 0 {
		return nil
	}
	released, _, releaseErr := releaseMutexPluginProc.Call(uintptr(l.handle))
	closed, _, closeErr := closeHandlePluginProc.Call(uintptr(l.handle))
	l.handle = 0
	if released == 0 {
		return fmt.Errorf("ReleaseMutex for plugin state: %w", releaseErr)
	}
	if closed == 0 {
		return fmt.Errorf("CloseHandle for plugin state: %w", closeErr)
	}
	return nil
}
