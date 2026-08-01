//go:build !windows

package filetrust

import (
	"fmt"
	"os"
	"syscall"
)

func validateOwnerControlled(_ string, info os.FileInfo, label string) error {
	if err := validateCurrentUserOwner(info, label); err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s must not be writable by group or other users", label)
	}
	return nil
}

func validateOwnerControlledDirectory(_ string, info os.FileInfo, label string) error {
	if err := validateCurrentUserOwner(info, label); err != nil {
		return err
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("%s must not be writable by group or other users", label)
	}
	return nil
}

func hardenOwnerControlled(path string) error {
	return os.Chmod(path, 0o600)
}

func hardenOwnerControlledDirectory(path string) error {
	return os.Chmod(path, 0o700)
}

func validateCurrentUserOwner(info os.FileInfo, label string) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s ownership is unavailable", label)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s must be owned by the current user", label)
	}
	return nil
}
