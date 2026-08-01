package filetrust

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func WriteJSONOwnerControlled(path string, value any) (returnedErr error) {
	directory := filepath.Dir(path)
	if err := EnsureOwnerControlledDirectory(directory, "owner-controlled data directory"); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".donethen-secure-*.tmp")
	if err != nil {
		return fmt.Errorf("create owner-controlled temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if returnedErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if err := HardenOwnerControlled(temporaryPath); err != nil {
		return err
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := replaceOwnerControlledFile(temporaryPath, path); err != nil {
		return err
	}
	if err := HardenOwnerControlled(path); err != nil {
		return fmt.Errorf("secure owner-controlled record: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect owner-controlled record: %w", err)
	}
	if err := ValidateOwnerControlled(path, info, "owner-controlled record"); err != nil {
		return err
	}
	return nil
}
