//go:build !windows

package filetrust

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnixOwnerControlledPermissionsFailClosedAndCanBeRepaired(t *testing.T) {
	root := filepath.Join(t.TempDir(), "loose")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o777); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerControlledDirectory(root, info, "loose directory"); err == nil {
		t.Fatal("validation accepted a directory writable by other users")
	}
	if err := EnsureOwnerControlledDirectory(root, "loose directory"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(root, "loose.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerControlled(path, info, "loose record"); err == nil {
		t.Fatal("validation accepted a file writable by other users")
	}
	if err := HardenOwnerControlled(path); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerControlled(path, info, "repaired record"); err != nil {
		t.Fatal(err)
	}
}
