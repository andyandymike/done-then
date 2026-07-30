//go:build windows

package codexexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func resolvePlatformExecutable(configured string) (Executable, error) {
	if configured == "" {
		configured = "codex"
	}
	path, err := resolvePath(configured)
	if err != nil {
		return Executable{}, fmt.Errorf("resolve Codex executable: %w", err)
	}
	extension := strings.ToLower(filepath.Ext(path))
	if extension == ".exe" || extension == ".com" {
		return Executable{Path: path}, nil
	}
	if executable, ok := resolveNPMShim(path); ok {
		return executable, nil
	}
	return Executable{}, fmt.Errorf("Codex path %q is a script wrapper; supply a native codex.exe or a standard npm Codex shim", path)
}

func resolvePath(configured string) (string, error) {
	if filepath.IsAbs(configured) || filepath.Dir(configured) != "." {
		absolute, err := filepath.Abs(configured)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(absolute); err != nil {
			return "", err
		}
		return absolute, nil
	}
	return exec.LookPath(configured)
}

func resolveNPMShim(shim string) (Executable, bool) {
	directory := filepath.Dir(shim)
	entrypoint := filepath.Join(directory, "node_modules", "@openai", "codex", "bin", "codex.js")
	if info, err := os.Stat(entrypoint); err != nil || info.IsDir() {
		return Executable{}, false
	}
	if native, ok := resolveNPMNative(directory); ok {
		return Executable{Path: native}, true
	}
	node := filepath.Join(directory, "node.exe")
	if info, err := os.Stat(node); err != nil || info.IsDir() {
		resolved, resolveErr := exec.LookPath("node.exe")
		if resolveErr != nil {
			return Executable{}, false
		}
		node = resolved
	}
	return Executable{Path: node, PrefixArgs: []string{entrypoint}}, true
}

func resolveNPMNative(shimDirectory string) (string, bool) {
	packageName := ""
	targetTriple := ""
	switch runtime.GOARCH {
	case "amd64":
		packageName = "codex-win32-x64"
		targetTriple = "x86_64-pc-windows-msvc"
	case "arm64":
		packageName = "codex-win32-arm64"
		targetTriple = "aarch64-pc-windows-msvc"
	default:
		return "", false
	}
	codexRoot := filepath.Join(shimDirectory, "node_modules", "@openai", "codex")
	candidates := []string{
		filepath.Join(codexRoot, "node_modules", "@openai", packageName, "vendor", targetTriple, "bin", "codex.exe"),
		filepath.Join(shimDirectory, "node_modules", "@openai", packageName, "vendor", targetTriple, "bin", "codex.exe"),
		filepath.Join(codexRoot, "vendor", targetTriple, "bin", "codex.exe"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}
