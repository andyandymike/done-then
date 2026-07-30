//go:build windows

package codexexeccontract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	. "github.com/andyandymike/done-then/internal/codexexec"
)

func TestResolveExecutableUsesNativeExecutableDirectly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable, err := ResolveExecutable(path)
	if err != nil {
		t.Fatal(err)
	}
	if executable.Path != path || len(executable.PrefixArgs) != 0 {
		t.Fatalf("ResolveExecutable() = %#v", executable)
	}
}

func TestResolveExecutableConvertsStandardNPMShimToNodeArgv(t *testing.T) {
	root := t.TempDir()
	shim := filepath.Join(root, "codex.cmd")
	node := filepath.Join(root, "node.exe")
	entrypoint := filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js")
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{shim, node, entrypoint} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := ResolveExecutable(shim)
	if err != nil {
		t.Fatal(err)
	}
	if executable.Path != node || len(executable.PrefixArgs) != 1 || executable.PrefixArgs[0] != entrypoint {
		t.Fatalf("ResolveExecutable() = %#v", executable)
	}
}

func TestResolveExecutablePrefersNPMNativeBinary(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("fixture models the Windows amd64 npm package layout")
	}
	root := t.TempDir()
	shim := filepath.Join(root, "codex.cmd")
	entrypoint := filepath.Join(root, "node_modules", "@openai", "codex", "bin", "codex.js")
	native := filepath.Join(
		root,
		"node_modules", "@openai", "codex", "node_modules", "@openai", "codex-win32-x64",
		"vendor", "x86_64-pc-windows-msvc", "bin", "codex.exe",
	)
	if err := os.MkdirAll(filepath.Dir(entrypoint), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(native), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{shim, entrypoint, native} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	executable, err := ResolveExecutable(shim)
	if err != nil {
		t.Fatal(err)
	}
	if executable.Path != native || len(executable.PrefixArgs) != 0 {
		t.Fatalf("ResolveExecutable() = %#v", executable)
	}
}
