package linuxpackagecontract

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLinuxInstallerIsPlanFirstAndRefusesUnresolvedPower(t *testing.T) {
	root := repositoryRoot(t)
	script := readText(t, filepath.Join(root, "scripts", "install-linux.sh"))
	for _, required := range []string{
		`action="plan"`, `--apply`, `EUID`, `active_state="/var/lib/donethen/active.json"`,
		`Refusing to %s while %s exists`, `systemd-tmpfiles --create`, `systemctl enable --now donethen-powerd.service`,
		`power_lock="$runtime_dir/power.lock"`, `flock -n "$power_lock_fd"`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("Linux installer is missing safety contract %q", required)
		}
	}
	for _, forbidden := range []string{"curl ", "wget ", "eval ", "sudo ", "systemctl poweroff", "shutdown -"} {
		if strings.Contains(script, forbidden) {
			t.Errorf("Linux installer contains forbidden operation %q", forbidden)
		}
	}
	activeCheck := strings.Index(script, `if [[ -e "$active_state" ]]`)
	installMutation := strings.Index(script, "groupadd --system donethen")
	if activeCheck < 0 || installMutation < 0 || activeCheck > installMutation {
		t.Fatal("unresolved helper state must be checked before installation mutates the host")
	}
}

func TestLinuxServiceAndRuntimeLockAreNarrow(t *testing.T) {
	root := repositoryRoot(t)
	service := readText(t, filepath.Join(root, "packaging", "linux", "donethen-powerd.service"))
	for _, required := range []string{
		"ExecStart=/usr/local/libexec/donethen/donethen-powerd",
		"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes",
		"RestrictAddressFamilies=AF_UNIX", "ReadWritePaths=/run/donethen /var/lib/donethen",
	} {
		if !strings.Contains(service, required) {
			t.Errorf("systemd service is missing hardening %q", required)
		}
	}
	if strings.Contains(service, "/bin/sh") || strings.Contains(service, "poweroff") {
		t.Fatal("systemd service must launch only the fixed helper, not a shell or power command")
	}
	tmpfiles := readText(t, filepath.Join(root, "packaging", "linux", "donethen.conf"))
	if !strings.Contains(tmpfiles, "d /run/donethen 0755 root root") ||
		!strings.Contains(tmpfiles, "f /run/donethen/power.lock 0660 root donethen") {
		t.Fatal("runtime directory and machine lock ownership contract changed")
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate Linux package contract test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
