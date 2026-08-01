//go:build windows

package powerpolicy

import (
	"fmt"
	"syscall"
	"unsafe"
)

var moveFileExProc = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replacePolicyFile(source, destination string) error {
	sourceUTF16, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationUTF16, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	const moveFileReplaceExisting = 0x1
	const moveFileWriteThrough = 0x8
	result, _, callErr := moveFileExProc.Call(
		uintptr(unsafe.Pointer(sourceUTF16)), uintptr(unsafe.Pointer(destinationUTF16)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return fmt.Errorf("MoveFileExW: %w", callErr)
	}
	return nil
}
