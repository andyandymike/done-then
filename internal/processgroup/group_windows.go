//go:build windows

package processgroup

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	jobObjectExtendedLimitInformation = 9
	jobObjectLimitKillOnJobClose      = 0x00002000
)

var (
	kernel32ProcessGroup         = syscall.NewLazyDLL("kernel32.dll")
	createJobObjectProc          = kernel32ProcessGroup.NewProc("CreateJobObjectW")
	setInformationJobObjectProc  = kernel32ProcessGroup.NewProc("SetInformationJobObject")
	assignProcessToJobObjectProc = kernel32ProcessGroup.NewProc("AssignProcessToJobObject")
	closeProcessGroupHandleProc  = kernel32ProcessGroup.NewProc("CloseHandle")
)

type basicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClass           uint32
	SchedulingClass         uint32
}

type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IOInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

type windowsGroup struct {
	handle syscall.Handle
}

func attachPlatform(process *os.Process) (Group, error) {
	if process == nil {
		return nil, fmt.Errorf("process is nil")
	}
	jobHandle, _, createErr := createJobObjectProc.Call(0, 0)
	if jobHandle == 0 {
		return nil, fmt.Errorf("CreateJobObjectW: %w", createErr)
	}
	group := &windowsGroup{handle: syscall.Handle(jobHandle)}
	info := extendedLimitInformation{}
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	result, _, setErr := setInformationJobObjectProc.Call(
		jobHandle,
		jobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		unsafe.Sizeof(info),
	)
	if result == 0 {
		_ = group.Close()
		return nil, fmt.Errorf("SetInformationJobObject: %w", setErr)
	}
	var assignErr error
	if err := process.WithHandle(func(processHandle uintptr) {
		assigned, _, callErr := assignProcessToJobObjectProc.Call(jobHandle, processHandle)
		if assigned == 0 {
			assignErr = fmt.Errorf("AssignProcessToJobObject: %w", callErr)
		}
	}); err != nil {
		_ = group.Close()
		return nil, fmt.Errorf("access process handle: %w", err)
	}
	if assignErr != nil {
		_ = group.Close()
		return nil, assignErr
	}
	return group, nil
}

func (g *windowsGroup) Close() error {
	if g.handle == 0 {
		return nil
	}
	result, _, callErr := closeProcessGroupHandleProc.Call(uintptr(g.handle))
	g.handle = 0
	if result == 0 {
		return fmt.Errorf("CloseHandle(job object): %w", callErr)
	}
	return nil
}
