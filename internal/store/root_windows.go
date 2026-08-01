//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"
)

func platformDefaultRoot() (string, error) {
	if root := os.Getenv("LOCALAPPDATA"); root != "" {
		return filepath.Join(root, "DoneThen"), nil
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate local application data: %w", err)
	}
	return filepath.Join(cache, "DoneThen"), nil
}
