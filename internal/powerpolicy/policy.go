package powerpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/andyandymike/done-then/internal/filetrust"
)

const maxPolicyBytes = 64 << 10

type Policy struct {
	SchemaVersion         int               `json:"schema_version"`
	ExecuteEnabled        bool              `json:"execute_enabled"`
	CodexExecutable       string            `json:"codex_executable"`
	CodexPrefixArgs       []string          `json:"codex_prefix_args,omitempty"`
	ExpectedPluginID      string            `json:"expected_plugin_id"`
	ExpectedHookHashes    map[string]string `json:"expected_hook_hashes"`
	AllowAgentOnlySuccess bool              `json:"allow_agent_only_success"`
	Fingerprint           string            `json:"-"`
}

func Path(dataRoot string) string {
	return filepath.Join(dataRoot, "power-policy.json")
}

func Load(dataRoot string) (Policy, error) {
	if strings.TrimSpace(dataRoot) == "" {
		return Policy{}, errors.New("power policy root is empty")
	}
	path, err := filepath.Abs(Path(dataRoot))
	if err != nil {
		return Policy{}, fmt.Errorf("resolve power policy: %w", err)
	}
	directory := filepath.Dir(path)
	directoryInfo, err := os.Lstat(directory)
	if err != nil {
		return Policy{}, fmt.Errorf("inspect power policy directory: %w", err)
	}
	if err := filetrust.ValidateOwnerControlledDirectory(directory, directoryInfo, "power policy directory"); err != nil {
		return Policy{}, err
	}
	file, info, err := filetrust.OpenOwnerControlled(path, "power policy")
	if err != nil {
		return Policy{}, err
	}
	defer file.Close()
	if info.Size() > maxPolicyBytes {
		return Policy{}, fmt.Errorf("power policy exceeds %d bytes", maxPolicyBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPolicyBytes+1))
	if err != nil {
		return Policy{}, fmt.Errorf("read power policy: %w", err)
	}
	if len(data) > maxPolicyBytes {
		return Policy{}, fmt.Errorf("power policy exceeds %d bytes", maxPolicyBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var policy Policy
	if err := decoder.Decode(&policy); err != nil {
		return Policy{}, fmt.Errorf("decode power policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Policy{}, errors.New("power policy contains trailing JSON")
	}
	if err := validate(policy); err != nil {
		return Policy{}, err
	}
	digest := sha256.Sum256(data)
	policy.Fingerprint = "sha256:" + hex.EncodeToString(digest[:])
	return policy, nil
}

func validate(policy Policy) error {
	if policy.SchemaVersion != 1 {
		return fmt.Errorf("unsupported power policy schema %d", policy.SchemaVersion)
	}
	if !policy.ExecuteEnabled {
		return errors.New("plugin execute is disabled by local power policy")
	}
	if !filepath.IsAbs(policy.CodexExecutable) {
		return errors.New("codex_executable must be an absolute path")
	}
	info, err := os.Lstat(policy.CodexExecutable)
	if err != nil {
		return fmt.Errorf("inspect configured Codex executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("configured Codex executable must be a regular file, not a link")
	}
	if len(policy.CodexPrefixArgs) > 8 {
		return errors.New("codex_prefix_args contains too many arguments")
	}
	for index, arg := range policy.CodexPrefixArgs {
		if strings.TrimSpace(arg) == "" || len(arg) > 4096 || strings.IndexByte(arg, 0) >= 0 {
			return errors.New("codex_prefix_args contains an invalid argument")
		}
		if index == 0 && strings.HasSuffix(strings.ToLower(arg), ".js") {
			if !filepath.IsAbs(arg) {
				return errors.New("Codex JavaScript entrypoint must be absolute")
			}
			entryInfo, entryErr := os.Lstat(arg)
			if entryErr != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 {
				return errors.New("Codex JavaScript entrypoint is unavailable or unsafe")
			}
		}
	}
	if strings.TrimSpace(policy.ExpectedPluginID) == "" || len(policy.ExpectedPluginID) > 256 {
		return errors.New("expected_plugin_id is required")
	}
	if len(policy.ExpectedHookHashes) == 0 || len(policy.ExpectedHookHashes) > 32 {
		return errors.New("expected_hook_hashes must contain 1 to 32 entries")
	}
	for key, hash := range policy.ExpectedHookHashes {
		if strings.TrimSpace(key) == "" || len(key) > 256 || strings.TrimSpace(hash) == "" || len(hash) > 256 {
			return errors.New("expected_hook_hashes contains an invalid key or hash")
		}
	}
	return nil
}

func Install(dataRoot string, policy Policy) (Policy, error) {
	policy.Fingerprint = ""
	if err := validate(policy); err != nil {
		return Policy{}, err
	}
	path, err := filepath.Abs(Path(dataRoot))
	if err != nil {
		return Policy{}, err
	}
	directory := filepath.Dir(path)
	if err := filetrust.EnsureOwnerControlledDirectory(directory, "power policy directory"); err != nil {
		return Policy{}, err
	}
	temporary, err := os.CreateTemp(directory, ".power-policy-*.tmp")
	if err != nil {
		return Policy{}, fmt.Errorf("create temporary power policy: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Policy{}, err
	}
	if err := filetrust.HardenOwnerControlled(temporaryPath); err != nil {
		return Policy{}, fmt.Errorf("protect temporary power policy: %w", err)
	}
	encoder := json.NewEncoder(temporary)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(policy); err != nil {
		return Policy{}, err
	}
	if err := temporary.Sync(); err != nil {
		return Policy{}, err
	}
	if err := temporary.Close(); err != nil {
		return Policy{}, err
	}
	if err := replacePolicyFile(temporaryPath, path); err != nil {
		return Policy{}, fmt.Errorf("replace power policy: %w", err)
	}
	if err := filetrust.HardenOwnerControlled(path); err != nil {
		return Policy{}, fmt.Errorf("protect power policy: %w", err)
	}
	committed = true
	installed, err := Load(dataRoot)
	if err != nil {
		return Policy{}, fmt.Errorf("verify installed power policy: %w", err)
	}
	return installed, nil
}
