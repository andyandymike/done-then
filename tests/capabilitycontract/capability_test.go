package capabilitycontract

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type manifest struct {
	SchemaVersion        int                           `json:"schema_version"`
	PluginExecuteDefault string                        `json:"plugin_execute_default"`
	Platforms            map[string]platformCapability `json:"platforms"`
}

type platformCapability struct {
	Level     string `json:"level"`
	Statement string `json:"statement"`
}

func TestPublishedCapabilityCopyMatchesRuntimeManifest(t *testing.T) {
	root := repositoryRoot(t)
	runtimeManifest := readFile(t, filepath.Join(root, "internal", "capability", "manifest.json"))
	pagesManifest := readFile(t, filepath.Join(root, "docs", "_data", "capabilities.json"))
	if !bytes.Equal(runtimeManifest, pagesManifest) {
		t.Fatal("docs/_data/capabilities.json must be an exact copy of the embedded runtime capability manifest")
	}
}

func TestPublicClaimsDoNotExceedManifest(t *testing.T) {
	root := repositoryRoot(t)
	payload := readFile(t, filepath.Join(root, "internal", "capability", "manifest.json"))
	var current manifest
	if err := json.Unmarshal(payload, &current); err != nil {
		t.Fatal(err)
	}
	if current.SchemaVersion != 1 || current.PluginExecuteDefault != "disabled" {
		t.Fatalf("unexpected capability safety defaults: %#v", current)
	}
	readme := string(readFile(t, filepath.Join(root, "README.md")))
	for platform, capability := range current.Platforms {
		for _, required := range []string{"`" + platform + "`", capability.Level, capability.Statement} {
			if !strings.Contains(readme, required) {
				t.Errorf("README capability table is missing %q for %s", required, platform)
			}
		}
	}
	for _, forbidden := range []string{"cross-platform supported", "Plugin power is supported", "macOS power support"} {
		if strings.Contains(readme, forbidden) {
			t.Errorf("README contains an unsupported aggregate capability claim %q", forbidden)
		}
	}
}

func TestReleasePackagesCapabilityEvidenceForEveryTarget(t *testing.T) {
	root := repositoryRoot(t)
	workflow := string(readFile(t, filepath.Join(root, ".github", "workflows", "release.yml")))
	if !strings.Contains(workflow, "internal/capability/manifest.json") || !strings.Contains(workflow, "CAPABILITIES.json") {
		t.Fatal("release archives must include the reviewed capability manifest")
	}
	if !strings.Contains(workflow, "./cmd/donethen-sbom") || !strings.Contains(workflow, ".spdx.json") {
		t.Fatal("every release target must publish a repository-generated SPDX SBOM")
	}
	for _, pair := range [][2]string{
		{"windows", "amd64"}, {"windows", "arm64"},
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
	} {
		if !strings.Contains(workflow, "_"+pair[0]+"_"+pair[1]) &&
			!(pair[0] == "windows" && pair[1] == "amd64" && strings.Contains(workflow, "windows_amd64")) {
			t.Errorf("release workflow does not publish %s-%s", pair[0], pair[1])
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate capability contract test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
