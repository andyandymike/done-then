//go:build !windows && !linux && !darwin

package store

import (
	"os"
	"path/filepath"
)

func platformDefaultRoot() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "DoneThen"), nil
}
