package verifierprofile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andyandymike/done-then/internal/filetrust"
	"github.com/andyandymike/done-then/internal/verifier"
)

const maxProfileBytes = 64 << 10

type Profile struct {
	SchemaVersion     int      `json:"schema_version"`
	ID                string   `json:"id"`
	Program           string   `json:"program"`
	Args              []string `json:"args"`
	WorkingDirectory  string   `json:"working_directory"`
	TimeoutSeconds    int64    `json:"timeout_seconds"`
	EnvironmentPolicy string   `json:"environment_policy"`
	ProgramSHA256     string   `json:"program_sha256,omitempty"`
	Fingerprint       string   `json:"-"`
}

type Registry struct {
	root string
}

func New(dataRoot string) (*Registry, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return nil, errors.New("verifier registry root is empty")
	}
	absolute, err := filepath.Abs(filepath.Join(dataRoot, "verifiers"))
	if err != nil {
		return nil, fmt.Errorf("resolve verifier registry: %w", err)
	}
	return &Registry{root: absolute}, nil
}

func (r *Registry) Root() string { return r.root }

func (r *Registry) Load(id string) (Profile, error) {
	if err := validateID(id); err != nil {
		return Profile{}, err
	}
	if err := r.validateRoot(false); err != nil {
		return Profile{}, err
	}
	path := filepath.Join(r.root, id+".json")
	file, info, err := filetrust.OpenOwnerControlled(path, "verifier profile "+id)
	if err != nil {
		return Profile{}, err
	}
	defer file.Close()
	if info.Size() > maxProfileBytes {
		return Profile{}, fmt.Errorf("verifier profile exceeds %d bytes", maxProfileBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxProfileBytes+1))
	if err != nil {
		return Profile{}, fmt.Errorf("read verifier profile %s: %w", id, err)
	}
	if len(data) > maxProfileBytes {
		return Profile{}, fmt.Errorf("verifier profile exceeds %d bytes", maxProfileBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var profile Profile
	if err := decoder.Decode(&profile); err != nil {
		return Profile{}, fmt.Errorf("decode verifier profile %s: %w", id, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Profile{}, errors.New("verifier profile contains trailing JSON")
	}
	if profile.ID != id {
		return Profile{}, errors.New("verifier profile id does not match its file name")
	}
	if err := validateProfile(profile); err != nil {
		return Profile{}, err
	}
	if profile.ProgramSHA256 != "" {
		digest, err := hashFile(profile.Program)
		if err != nil {
			return Profile{}, fmt.Errorf("hash verifier program: %w", err)
		}
		expected := strings.ToLower(profile.ProgramSHA256)
		if !strings.HasPrefix(expected, "sha256:") {
			expected = "sha256:" + expected
		}
		if !strings.EqualFold(expected, digest) {
			return Profile{}, errors.New("verifier program hash does not match the registered profile")
		}
	}
	digest := sha256.Sum256(data)
	profile.Fingerprint = "sha256:" + hex.EncodeToString(digest[:])
	return profile, nil
}

func (r *Registry) List() ([]string, error) {
	if err := r.validateRoot(true); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(r.root)
	if errors.Is(err, os.ErrNotExist) {
		return []string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list verifier profiles: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if err := validateID(id); err == nil {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (r *Registry) Install(profile Profile) (Profile, error) {
	profile.Fingerprint = ""
	if err := validateProfile(profile); err != nil {
		return Profile{}, err
	}
	if err := filetrust.EnsureOwnerControlledDirectory(r.root, "verifier profile directory"); err != nil {
		return Profile{}, err
	}
	path := filepath.Join(r.root, profile.ID+".json")
	if err := filetrust.WriteJSONOwnerControlled(path, profile); err != nil {
		return Profile{}, fmt.Errorf("install verifier profile: %w", err)
	}
	return r.Load(profile.ID)
}

func (r *Registry) validateRoot(allowMissing bool) error {
	info, err := os.Lstat(r.root)
	if allowMissing && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect verifier profile directory: %w", err)
	}
	return filetrust.ValidateOwnerControlledDirectory(r.root, info, "verifier profile directory")
}

func Validate(profile Profile) error { return validateProfile(profile) }

func (p Profile) Runner(workspace string, stdout, stderr io.Writer) (*verifier.Runner, error) {
	if !filepath.IsAbs(workspace) {
		return nil, errors.New("verifier workspace must be absolute")
	}
	args := make([]string, len(p.Args))
	for index, value := range p.Args {
		switch value {
		case "{workspace}":
			args[index] = workspace
		default:
			if strings.Contains(value, "{") || strings.Contains(value, "}") {
				return nil, fmt.Errorf("unsupported verifier placeholder in argument %d", index)
			}
			args[index] = value
		}
	}
	return &verifier.Runner{
		Program: p.Program,
		Args:    args,
		Dir:     workspace,
		Timeout: time.Duration(p.TimeoutSeconds) * time.Second,
		Env:     minimalEnvironment(),
		Stdout:  stdout,
		Stderr:  stderr,
	}, nil
}

func validateProfile(profile Profile) error {
	if profile.SchemaVersion != 1 {
		return fmt.Errorf("unsupported verifier profile schema %d", profile.SchemaVersion)
	}
	if err := validateID(profile.ID); err != nil {
		return err
	}
	if !filepath.IsAbs(profile.Program) {
		return errors.New("verifier program must be an absolute path")
	}
	info, err := os.Lstat(profile.Program)
	if err != nil {
		return fmt.Errorf("inspect verifier program: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("verifier program must be a regular file, not a link")
	}
	if profile.WorkingDirectory != "armed_workspace" {
		return errors.New("working_directory must be armed_workspace")
	}
	if profile.TimeoutSeconds < 1 || profile.TimeoutSeconds > 3600 {
		return errors.New("timeout_seconds must be between 1 and 3600")
	}
	if profile.EnvironmentPolicy != "minimal" {
		return errors.New("environment_policy must be minimal")
	}
	if len(profile.Args) > 128 {
		return errors.New("verifier profile has too many arguments")
	}
	for _, arg := range profile.Args {
		if len(arg) > 4096 || strings.IndexByte(arg, 0) >= 0 {
			return errors.New("verifier argument is invalid")
		}
	}
	if profile.ProgramSHA256 != "" {
		value := strings.TrimPrefix(strings.ToLower(profile.ProgramSHA256), "sha256:")
		if len(value) != 64 {
			return errors.New("program_sha256 must contain a SHA-256 digest")
		}
		if _, err := hex.DecodeString(value); err != nil {
			return errors.New("program_sha256 must contain a SHA-256 digest")
		}
	}
	return nil
}

func validateID(id string) error {
	if len(id) < 1 || len(id) > 64 {
		return errors.New("verifier profile id must contain 1 to 64 characters")
	}
	for _, char := range id {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '_' || char == '-' {
			continue
		}
		return errors.New("verifier profile id contains an invalid character")
	}
	return nil
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
