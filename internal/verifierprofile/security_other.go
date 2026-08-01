//go:build !windows

package verifierprofile

import (
	"os"

	"github.com/andyandymike/done-then/internal/filetrust"
)

func validateProfileFileSecurity(path string, info os.FileInfo) error {
	return filetrust.ValidateOwnerControlled(path, info, "verifier profile")
}
