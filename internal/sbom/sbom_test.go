package sbom

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestGenerateDeterministicSPDXDocument(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "donethen_0.1.0_linux_amd64.tar.gz")
	content := []byte("reviewed release archive")
	if err := os.WriteFile(artifact, content, 0o600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	created := time.Date(2026, time.July, 31, 1, 2, 3, 0, time.UTC)
	options := Options{
		ArtifactPath: artifact,
		BinaryPath:   binary,
		Version:      "0.1.0-alpha",
		GOOS:         runtime.GOOS,
		GOARCH:       runtime.GOARCH,
		Created:      created,
	}
	first, err := Generate(options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(options)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("SBOM generation is not deterministic")
	}
	if !json.Valid(first) {
		t.Fatal("generated SBOM is not valid JSON")
	}
	var document Document
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	if document.SPDXVersion != "SPDX-2.3" || document.DataLicense != "CC0-1.0" {
		t.Fatalf("unexpected SPDX header: %#v", document)
	}
	if len(document.DocumentDescribes) != 1 || document.DocumentDescribes[0] != mainPackageID {
		t.Fatalf("unexpected document scope: %#v", document.DocumentDescribes)
	}
	if len(document.Files) != 1 || document.Files[0].FileName != "./"+filepath.Base(artifact) {
		t.Fatalf("unexpected artifact file entry: %#v", document.Files)
	}
	sha1Sum := sha1.Sum(content)
	sha256Sum := sha256.Sum256(content)
	expectedSHA1 := hex.EncodeToString(sha1Sum[:])
	expectedSHA256 := hex.EncodeToString(sha256Sum[:])
	assertChecksum(t, document.Files[0].Checksums, "SHA1", expectedSHA1)
	assertChecksum(t, document.Files[0].Checksums, "SHA256", expectedSHA256)
	if !strings.HasSuffix(document.DocumentNamespace, "/"+expectedSHA256) {
		t.Fatalf("namespace is not bound to the artifact digest: %s", document.DocumentNamespace)
	}
	if len(document.Packages) == 0 || document.Packages[0].LicenseDeclared != "Apache-2.0" {
		t.Fatalf("unexpected primary package: %#v", document.Packages)
	}
	verificationInput := sha1.Sum([]byte(expectedSHA1))
	if got := document.Packages[0].PackageVerificationCode; got == nil || got.Value != hex.EncodeToString(verificationInput[:]) {
		t.Fatalf("unexpected package verification code: %#v", got)
	}
}

func TestGenerateRejectsMismatchedBuildTarget(t *testing.T) {
	root := t.TempDir()
	artifact := filepath.Join(root, "artifact.zip")
	if err := os.WriteFile(artifact, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Generate(Options{
		ArtifactPath: artifact,
		BinaryPath:   binary,
		Version:      "0.1.0",
		GOOS:         "mismatched",
		GOARCH:       runtime.GOARCH,
		Created:      time.Now(),
	})
	if err == nil || !strings.Contains(err.Error(), "binary target") {
		t.Fatalf("expected target mismatch, got %v", err)
	}
}

func TestGenerateRejectsInvalidMetadata(t *testing.T) {
	_, err := Generate(Options{Version: "../unsafe"})
	if err == nil || !strings.Contains(err.Error(), "artifact path is required") {
		t.Fatalf("expected required-field validation, got %v", err)
	}
}

func assertChecksum(t *testing.T, checksums []Checksum, algorithm, expected string) {
	t.Helper()
	for _, checksum := range checksums {
		if checksum.Algorithm == algorithm {
			if checksum.Value != expected {
				t.Fatalf("%s checksum = %s, want %s", algorithm, checksum.Value, expected)
			}
			return
		}
	}
	t.Fatalf("missing %s checksum", algorithm)
}
