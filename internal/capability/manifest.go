package capability

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed manifest.json
var manifestData []byte

type Platform struct {
	Level     string `json:"level"`
	Statement string `json:"statement"`
}

type Manifest struct {
	SchemaVersion                 int                 `json:"schema_version"`
	Updated                       string              `json:"updated"`
	PluginExecuteDefault          string              `json:"plugin_execute_default"`
	VerifiedSuccessExecuteDefault string              `json:"verified_success_execute_default"`
	Platforms                     map[string]Platform `json:"platforms"`
}

func Load() (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode embedded capability manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.PluginExecuteDefault != "after_stop_on_supported_platforms" ||
		manifest.VerifiedSuccessExecuteDefault != "disabled" || len(manifest.Platforms) == 0 {
		return Manifest{}, fmt.Errorf("embedded capability manifest is invalid")
	}
	return manifest, nil
}

func Current(goos, goarch string) (Platform, bool, error) {
	manifest, err := Load()
	if err != nil {
		return Platform{}, false, err
	}
	platform, ok := manifest.Platforms[goos+"-"+goarch]
	return platform, ok, nil
}
