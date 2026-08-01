package powerpolicy

import (
	"os"
	"testing"
)

func TestInstallCreatesOwnerControlledRoundTrippablePolicy(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	installed, err := Install(root, Policy{
		SchemaVersion: 1, ExecuteEnabled: true, CodexExecutable: executable,
		ExpectedPluginID: "done-then", ExpectedHookHashes: map[string]string{"stop": "sha256:hook"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if installed.Fingerprint == "" || installed.ExpectedPluginID != "done-then" {
		t.Fatalf("installed policy = %#v", installed)
	}
	reloaded, err := Load(root)
	if err != nil || reloaded.Fingerprint != installed.Fingerprint {
		t.Fatalf("reloaded policy = %#v, %v", reloaded, err)
	}
}
