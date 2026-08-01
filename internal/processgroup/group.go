package processgroup

import (
	"os"
	"os/exec"
)

type Group interface {
	Close() error
}

func Prepare(command *exec.Cmd) error {
	return preparePlatform(command)
}

func Attach(process *os.Process) (Group, error) {
	return attachPlatform(process)
}
