//go:build darwin

package store

import (
	"os"
	"path/filepath"
)

func platformDefaultRoot() (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "DoneThen"), nil
}
