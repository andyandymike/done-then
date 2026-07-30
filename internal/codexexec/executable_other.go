//go:build !windows

package codexexec

import (
	"fmt"
	"os/exec"
)

func resolvePlatformExecutable(configured string) (Executable, error) {
	if configured == "" {
		configured = "codex"
	}
	path, err := exec.LookPath(configured)
	if err != nil {
		return Executable{}, fmt.Errorf("resolve Codex executable: %w", err)
	}
	return Executable{Path: path}, nil
}
