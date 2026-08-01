//go:build linux

package store

import (
	"errors"
	"os"
	"path/filepath"
)

func platformDefaultRoot() (string, error) {
	if root := os.Getenv("XDG_STATE_HOME"); root != "" {
		if !filepath.IsAbs(root) {
			return "", errors.New("XDG_STATE_HOME must be absolute")
		}
		return filepath.Join(root, "donethen"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "donethen"), nil
}
