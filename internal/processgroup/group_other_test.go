//go:build !windows

package processgroup

import (
	"os/exec"
	"testing"
)

func TestPrepareCreatesIsolatedUnixProcessGroup(t *testing.T) {
	command := exec.Command("true")
	if err := Prepare(command); err != nil {
		t.Fatal(err)
	}
	if command.SysProcAttr == nil || !command.SysProcAttr.Setpgid || command.SysProcAttr.Pgid != 0 {
		t.Fatalf("process group attributes = %#v", command.SysProcAttr)
	}
}
