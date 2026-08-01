package filetrust

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHardenedFilePassesOwnerControlledValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := HardenOwnerControlled(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerControlled(path, info, "test policy"); err != nil {
		t.Fatal(err)
	}
}

func TestOwnerControlledDirectoryAndOpenHelpers(t *testing.T) {
	root := filepath.Join(t.TempDir(), "secure")
	if err := EnsureOwnerControlledDirectory(root, "test data root"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerControlledDirectory(root, info, "test data root"); err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(root, "events.log")
	logFile, err := OpenAppendOwnerControlled(logPath, "test event log")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := logFile.WriteString("event\n"); err != nil {
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	readFile, readInfo, err := OpenOwnerControlled(logPath, "test event log")
	if err != nil {
		t.Fatal(err)
	}
	defer readFile.Close()
	if readInfo.Size() == 0 {
		t.Fatal("owner-controlled append did not persist data")
	}
}

func TestOwnerControlledHelpersRejectLinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(target, link); err != nil {
		if errors.Is(err, os.ErrPermission) {
			t.Skip("symbolic-link creation is not permitted on this host")
		}
		t.Skipf("symbolic-link creation is unavailable: %v", err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateOwnerControlled(link, info, "linked record"); err == nil {
		t.Fatal("owner-controlled validation accepted a symbolic link")
	}
	if err := HardenOwnerControlled(link); err == nil {
		t.Fatal("owner-controlled hardening followed a symbolic link")
	}
}
