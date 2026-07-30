//go:build !windows

package processgroup

import "os"

type noOpGroup struct{}

func attachPlatform(*os.Process) (Group, error) {
	return noOpGroup{}, nil
}

func (noOpGroup) Close() error {
	return nil
}
