//go:build windows

package filetrust

import (
	"fmt"
	"syscall"
	"unsafe"
)

var moveOwnerFileExProc = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replaceOwnerControlledFile(source, destination string) error {
	sourceUTF16, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationUTF16, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	const replaceExisting = 0x1
	const writeThrough = 0x8
	result, _, callErr := moveOwnerFileExProc.Call(
		uintptr(unsafe.Pointer(sourceUTF16)), uintptr(unsafe.Pointer(destinationUTF16)), replaceExisting|writeThrough,
	)
	if result == 0 {
		return fmt.Errorf("MoveFileExW: %w", callErr)
	}
	return nil
}
