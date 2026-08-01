//go:build !windows

package hostauthority

import (
	"os/exec"
	"syscall"
)

func configureProxyProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
