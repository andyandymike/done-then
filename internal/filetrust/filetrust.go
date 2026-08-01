package filetrust

import (
	"fmt"
	"os"
	"path/filepath"
)

// ValidateOwnerControlled verifies that path names an owner-controlled regular
// file. Callers should pass os.Lstat output so links are rejected rather than
// followed.
func ValidateOwnerControlled(path string, info os.FileInfo, label string) error {
	if info == nil {
		return fmt.Errorf("inspect %s: file information is missing", label)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s must be a regular file, not a link or special file", label)
	}
	return validateOwnerControlled(path, info, label)
}

// ValidateOwnerControlledDirectory verifies the ownership and write boundary
// of a real directory. Reparse points and symbolic links are never accepted.
func ValidateOwnerControlledDirectory(path string, info os.FileInfo, label string) error {
	if info == nil {
		return fmt.Errorf("inspect %s: directory information is missing", label)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%s must be a directory, not a link or special file", label)
	}
	return validateOwnerControlledDirectory(path, info, label)
}

func HardenOwnerControlled(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect owner-controlled file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("owner-controlled file must be a regular file, not a link or special file")
	}
	return hardenOwnerControlled(path)
}

func HardenOwnerControlledDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect owner-controlled directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("owner-controlled directory must be a directory, not a link or special file")
	}
	return hardenOwnerControlledDirectory(path)
}

// EnsureOwnerControlledDirectory creates a directory tree if needed and then
// narrows and verifies the final directory. Callers should invoke this for each
// security boundary in a data-root hierarchy; securing only a leaf is not
// sufficient when an ancestor can be replaced.
func EnsureOwnerControlledDirectory(path, label string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", label, err)
	}
	if err := HardenOwnerControlledDirectory(path); err != nil {
		return fmt.Errorf("secure %s: %w", label, err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", label, err)
	}
	if err := ValidateOwnerControlledDirectory(path, info, label); err != nil {
		return err
	}
	return nil
}

// OpenOwnerControlled opens an already-existing trusted record and verifies
// that the opened handle still refers to the file inspected through its path.
func OpenOwnerControlled(path, label string) (*os.File, os.FileInfo, error) {
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if err := ValidateOwnerControlled(path, pathInfo, label); err != nil {
		return nil, nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", label, err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("inspect opened %s: %w", label, err)
	}
	if !os.SameFile(pathInfo, fileInfo) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s changed while it was being opened", label)
	}
	return file, fileInfo, nil
}

// OpenAppendOwnerControlled creates or opens an append-only log only after its
// containing directory and resulting file have been narrowed and verified.
func OpenAppendOwnerControlled(path, label string) (*os.File, error) {
	directory := filepath.Dir(path)
	if err := EnsureOwnerControlledDirectory(directory, label+" directory"); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	closeOnError := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}
	if err := HardenOwnerControlled(path); err != nil {
		return closeOnError(fmt.Errorf("secure %s: %w", label, err))
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return closeOnError(fmt.Errorf("inspect %s: %w", label, err))
	}
	if err := ValidateOwnerControlled(path, pathInfo, label); err != nil {
		return closeOnError(err)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("inspect opened %s: %w", label, err))
	}
	if !os.SameFile(pathInfo, fileInfo) {
		return closeOnError(fmt.Errorf("%s changed while it was being opened", label))
	}
	return file, nil
}
