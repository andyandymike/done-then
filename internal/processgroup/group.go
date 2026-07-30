package processgroup

import "os"

type Group interface {
	Close() error
}

func Attach(process *os.Process) (Group, error) {
	return attachPlatform(process)
}
