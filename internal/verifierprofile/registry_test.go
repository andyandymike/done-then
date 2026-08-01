package verifierprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/andyandymike/done-then/internal/filetrust"
)

func TestRegistryLoadsStrictProfileAndBuildsRunner(t *testing.T) {
	root := t.TempDir()
	registry, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := filetrust.EnsureOwnerControlledDirectory(registry.Root(), "test verifier directory"); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(program)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	profile := Profile{
		SchemaVersion:     1,
		ID:                "repo-tests",
		Program:           program,
		Args:              []string{"--workspace", "{workspace}"},
		WorkingDirectory:  "armed_workspace",
		TimeoutSeconds:    60,
		EnvironmentPolicy: "minimal",
		ProgramSHA256:     hex.EncodeToString(digest[:]),
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(registry.Root(), profile.ID+".json")
	if err := os.WriteFile(profilePath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := filetrust.HardenOwnerControlled(profilePath); err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Fingerprint == "" {
		t.Fatal("profile fingerprint is empty")
	}
	workspace, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner, err := loaded.Runner(workspace, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if runner.Dir != workspace || runner.Args[1] != workspace || runner.Env == nil {
		t.Fatalf("runner = %#v", runner)
	}
	ids, err := registry.List()
	if err != nil || len(ids) != 1 || ids[0] != profile.ID {
		t.Fatalf("List() = %#v, %v", ids, err)
	}
}

func TestRegistryRejectsUnknownFieldsAndUnsafeIDs(t *testing.T) {
	root := t.TempDir()
	registry, _ := New(root)
	if err := filetrust.EnsureOwnerControlledDirectory(registry.Root(), "test verifier directory"); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Load("../escape"); err == nil {
		t.Fatal("unsafe profile id was accepted")
	}
	path := filepath.Join(registry.Root(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":1,"id":"bad","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Load("bad"); err == nil {
		t.Fatal("unknown profile field was accepted")
	}
}
