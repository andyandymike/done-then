//go:build !windows

package processgroup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

type unixGroup struct {
	processGroupID int
}

func preparePlatform(command *exec.Cmd) error {
	if command == nil {
		return errors.New("command is nil")
	}
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	attributes.Setpgid = true
	attributes.Pgid = 0
	command.SysProcAttr = &attributes
	return nil
}

func attachPlatform(process *os.Process) (Group, error) {
	if process == nil {
		return nil, errors.New("process is nil")
	}
	// Prepare requests Setpgid with Pgid=0, so the child PID is the group ID.
	// Avoid Getpgid here: a very short-lived child can exit between Start and
	// Attach while its descendants still need the stable group identifier.
	return &unixGroup{processGroupID: process.Pid}, nil
}

func (g *unixGroup) Close() error {
	if g.processGroupID <= 0 {
		return nil
	}
	err := syscall.Kill(-g.processGroupID, syscall.SIGKILL)
	g.processGroupID = 0
	if err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("terminate process group: %w", err)
	}
	return nil
}
