package sbom

import (
	"crypto/sha1"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"time"
)

const (
	projectModule = "github.com/andyandymike/done-then"
	documentID    = "SPDXRef-DOCUMENT"
	mainPackageID = "SPDXRef-Package-DoneThen"
	artifactID    = "SPDXRef-File-ReleaseArtifact"
)

var tokenPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._-]{0,127}$`)

type Options struct {
	ArtifactPath string
	BinaryPath   string
	Version      string
	GOOS         string
	GOARCH       string
	Created      time.Time
}

type Document struct {
	SPDXVersion       string         `json:"spdxVersion"`
	DataLicense       string         `json:"dataLicense"`
	SPDXID            string         `json:"SPDXID"`
	Name              string         `json:"name"`
	DocumentNamespace string         `json:"documentNamespace"`
	CreationInfo      CreationInfo   `json:"creationInfo"`
	DocumentDescribes []string       `json:"documentDescribes"`
	Packages          []Package      `json:"packages"`
	Files             []File         `json:"files"`
	Relationships     []Relationship `json:"relationships"`
}

type CreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type Package struct {
	Name                    string                   `json:"name"`
	SPDXID                  string                   `json:"SPDXID"`
	VersionInfo             string                   `json:"versionInfo,omitempty"`
	DownloadLocation        string                   `json:"downloadLocation"`
	FilesAnalyzed           bool                     `json:"filesAnalyzed"`
	PackageVerificationCode *PackageVerificationCode `json:"packageVerificationCode,omitempty"`
	Checksums               []Checksum               `json:"checksums,omitempty"`
	LicenseConcluded        string                   `json:"licenseConcluded"`
	LicenseDeclared         string                   `json:"licenseDeclared"`
	CopyrightText           string                   `json:"copyrightText"`
	Comment                 string                   `json:"comment,omitempty"`
}

type PackageVerificationCode struct {
	Value string `json:"packageVerificationCodeValue"`
}

type File struct {
	FileName          string     `json:"fileName"`
	SPDXID            string     `json:"SPDXID"`
	Checksums         []Checksum `json:"checksums"`
	LicenseConcluded  string     `json:"licenseConcluded"`
	LicenseInfoInFile []string   `json:"licenseInfoInFiles"`
	CopyrightText     string     `json:"copyrightText"`
}

type Checksum struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"checksumValue"`
}

type Relationship struct {
	Element string `json:"spdxElementId"`
	Type    string `json:"relationshipType"`
	Related string `json:"relatedSpdxElement"`
}

func Generate(options Options) ([]byte, error) {
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	artifactSHA1, artifactSHA256, err := hashFile(options.ArtifactPath)
	if err != nil {
		return nil, fmt.Errorf("hash release artifact: %w", err)
	}
	info, err := buildinfo.ReadFile(options.BinaryPath)
	if err != nil {
		return nil, fmt.Errorf("read Go build info: %w", err)
	}
	if info.Main.Path != projectModule {
		return nil, fmt.Errorf("binary main module %q does not match %q", info.Main.Path, projectModule)
	}
	if err := validateBuildTarget(info.Settings, options.GOOS, options.GOARCH); err != nil {
		return nil, err
	}

	checksums := []Checksum{
		{Algorithm: "SHA1", Value: artifactSHA1},
		{Algorithm: "SHA256", Value: artifactSHA256},
	}
	verificationInput := sha1.Sum([]byte(artifactSHA1))
	packages := []Package{{
		Name:             "DoneThen",
		SPDXID:           mainPackageID,
		VersionInfo:      options.Version,
		DownloadLocation: "NOASSERTION",
		FilesAnalyzed:    true,
		PackageVerificationCode: &PackageVerificationCode{
			Value: hex.EncodeToString(verificationInput[:]),
		},
		Checksums:        checksums,
		LicenseConcluded: "Apache-2.0",
		LicenseDeclared:  "Apache-2.0",
		CopyrightText:    "NOASSERTION",
	}}
	relationships := []Relationship{
		{Element: documentID, Type: "DESCRIBES", Related: mainPackageID},
		{Element: mainPackageID, Type: "CONTAINS", Related: artifactID},
	}

	dependencies := append([]*debug.Module(nil), info.Deps...)
	sort.Slice(dependencies, func(i, j int) bool {
		return dependencyKey(dependencies[i]) < dependencyKey(dependencies[j])
	})
	for _, dependency := range dependencies {
		pkg, relationship := dependencyPackage(dependency)
		packages = append(packages, pkg)
		relationships = append(relationships, relationship)
	}

	artifactName := filepath.Base(options.ArtifactPath)
	document := Document{
		SPDXVersion:       "SPDX-2.3",
		DataLicense:       "CC0-1.0",
		SPDXID:            documentID,
		Name:              fmt.Sprintf("DoneThen %s %s-%s", options.Version, options.GOOS, options.GOARCH),
		DocumentNamespace: fmt.Sprintf("https://github.com/andyandymike/done-then/spdx/%s/%s-%s/%s", options.Version, options.GOOS, options.GOARCH, artifactSHA256),
		CreationInfo: CreationInfo{
			Created:  options.Created.UTC().Format(time.RFC3339),
			Creators: []string{"Tool: donethen-sbom-" + options.Version},
		},
		DocumentDescribes: []string{mainPackageID},
		Packages:          packages,
		Files: []File{{
			FileName:          "./" + artifactName,
			SPDXID:            artifactID,
			Checksums:         checksums,
			LicenseConcluded:  "NOASSERTION",
			LicenseInfoInFile: []string{"NOASSERTION"},
			CopyrightText:     "NOASSERTION",
		}},
		Relationships: relationships,
	}
	payload, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode SPDX document: %w", err)
	}
	return append(payload, '\n'), nil
}

func validateOptions(options Options) error {
	required := []struct {
		name  string
		value string
	}{
		{name: "artifact path", value: options.ArtifactPath},
		{name: "binary path", value: options.BinaryPath},
		{name: "version", value: options.Version},
		{name: "goos", value: options.GOOS},
		{name: "goarch", value: options.GOARCH},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	for _, field := range required[2:] {
		if !tokenPattern.MatchString(field.value) {
			return fmt.Errorf("%s contains unsupported characters", field.name)
		}
	}
	if options.Created.IsZero() {
		return errors.New("creation time is required")
	}
	return nil
}

func validateBuildTarget(settings []debug.BuildSetting, expectedOS, expectedArch string) error {
	actual := make(map[string]string)
	for _, setting := range settings {
		if setting.Key == "GOOS" || setting.Key == "GOARCH" {
			actual[setting.Key] = setting.Value
		}
	}
	if actual["GOOS"] != expectedOS || actual["GOARCH"] != expectedArch {
		return fmt.Errorf("binary target %s-%s does not match requested %s-%s", actual["GOOS"], actual["GOARCH"], expectedOS, expectedArch)
	}
	return nil
}

func hashFile(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	sha1Hash := sha1.New()
	sha256Hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(sha1Hash, sha256Hash), file); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(sha1Hash.Sum(nil)), hex.EncodeToString(sha256Hash.Sum(nil)), nil
}

func dependencyKey(module *debug.Module) string {
	if module == nil {
		return ""
	}
	return module.Path + "@" + module.Version
}

func dependencyPackage(module *debug.Module) (Package, Relationship) {
	key := dependencyKey(module)
	sum := sha256.Sum256([]byte(key))
	id := "SPDXRef-Package-Go-" + hex.EncodeToString(sum[:8])
	comment := ""
	if module.Replace != nil {
		comment = fmt.Sprintf("Go module replacement: %s@%s", module.Replace.Path, module.Replace.Version)
	}
	pkg := Package{
		Name:             module.Path,
		SPDXID:           id,
		VersionInfo:      module.Version,
		DownloadLocation: "NOASSERTION",
		FilesAnalyzed:    false,
		LicenseConcluded: "NOASSERTION",
		LicenseDeclared:  "NOASSERTION",
		CopyrightText:    "NOASSERTION",
		Comment:          comment,
	}
	return pkg, Relationship{Element: mainPackageID, Type: "DEPENDS_ON", Related: id}
}
