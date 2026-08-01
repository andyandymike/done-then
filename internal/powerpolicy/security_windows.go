//go:build windows

package powerpolicy

import (
	"os"

	"github.com/andyandymike/done-then/internal/filetrust"
)

func validatePolicyFileSecurity(path string, info os.FileInfo) error {
	return filetrust.ValidateOwnerControlled(path, info, "power policy")
}
