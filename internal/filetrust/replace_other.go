//go:build !windows

package filetrust

import "os"

func replaceOwnerControlledFile(source, destination string) error {
	return os.Rename(source, destination)
}
