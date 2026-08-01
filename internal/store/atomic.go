package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/andyandymike/done-then/internal/filetrust"
)

func atomicWriteJSON(path string, value any) (returnedErr error) {
	directory := filepath.Dir(path)
	if err := filetrust.EnsureOwnerControlledDirectory(directory, "record directory"); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".donethen-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary record: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if returnedErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary record permissions: %w", err)
	}
	if err := filetrust.HardenOwnerControlled(temporaryPath); err != nil {
		return fmt.Errorf("secure temporary record: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return fmt.Errorf("encode record: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("flush record: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close record: %w", err)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace record: %w", err)
	}
	if err := filetrust.HardenOwnerControlled(path); err != nil {
		return fmt.Errorf("secure record: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect record: %w", err)
	}
	if err := filetrust.ValidateOwnerControlled(path, info, "record"); err != nil {
		return err
	}
	return nil
}

// AtomicWriteJSON writes a JSON record by flushing a temporary file and then
// replacing the destination. It is exported for other DoneThen state stores
// that need the same crash-safe replacement semantics.
func AtomicWriteJSON(path string, value any) error {
	return atomicWriteJSON(path, value)
}
