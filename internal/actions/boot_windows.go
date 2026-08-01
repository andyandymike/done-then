//go:build windows

package actions

import (
	"fmt"
	"syscall"
	"time"
)

var getTickCount64 = syscall.NewLazyDLL("kernel32.dll").NewProc("GetTickCount64")

func platformBootID() string {
	ticks, _, callErr := getTickCount64.Call()
	if callErr != syscall.Errno(0) {
		return ""
	}
	bootTime := time.Now().UTC().Add(-time.Duration(uint64(ticks)) * time.Millisecond)
	// GetTickCount64 and wall-clock reads are not atomic. A one-minute bucket keeps
	// the identifier stable across ordinary calls while still separating boots.
	return fmt.Sprintf("windows-boot-%d", bootTime.Unix()/60)
}
