//go:build windows

package hostauthority

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

func configureProxyProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
