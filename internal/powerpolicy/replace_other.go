//go:build !windows

package powerpolicy

import "os"

func replacePolicyFile(source, destination string) error {
	return os.Rename(source, destination)
}
