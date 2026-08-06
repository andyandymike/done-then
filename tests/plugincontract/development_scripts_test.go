package plugincontract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDevelopmentScriptsPlanAndWhatIfAreNonMutating(t *testing.T) {
	requireWindowsPowerShell(t)
	root := repositoryRoot(t)
	fixture := newPluginRepositoryFixture(t, root)

	devScript := filepath.Join(root, "scripts", "dev-plugin.ps1")
	output := runPowerShell(t, nil, devScript, "-Action", "Plan", "-RepositoryRoot", fixture)
	if !strings.Contains(output, `"direct_user_file_edits": false`) {
		t.Fatalf("plan did not report its direct-write boundary:\n%s", output)
	}
	runPowerShell(t, nil, devScript, "-Action", "Install", "-RepositoryRoot", fixture, "-Apply", "-WhatIf")
	if _, err := os.Stat(filepath.Join(fixture, ".tmp", "done-then-dev-marketplace")); !os.IsNotExist(err) {
		t.Fatalf("WhatIf created the development marketplace: %v", err)
	}

	smokeScript := filepath.Join(root, "scripts", "live-smoke.ps1")
	runPowerShell(t, nil, smokeScript, "-Action", "Plan", "-RepositoryRoot", fixture)
	runPowerShell(t, nil, smokeScript, "-Action", "Snapshot", "-RepositoryRoot", fixture, "-Apply", "-WhatIf")
	if _, err := os.Stat(filepath.Join(fixture, ".tmp", "live-smoke", "baseline.json")); !os.IsNotExist(err) {
		t.Fatalf("WhatIf created the live-smoke baseline: %v", err)
	}
}

func TestDevelopmentInstallStagesAndUninstallsOnlyOwnedMarketplace(t *testing.T) {
	requireWindowsPowerShell(t)
	root := repositoryRoot(t)
	fixture := newPluginRepositoryFixture(t, root)
	statePath := filepath.Join(t.TempDir(), "fake-codex-state.json")
	fakeCodex := newFakeCodexCommand(t)
	runtimePath := filepath.Join(t.TempDir(), "reviewed-donethen-runtime.cmd")
	if err := os.WriteFile(runtimePath, []byte("@echo off\r\nexit /b 0\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	environment := []string{"DONETHEN_FAKE_CODEX_STATE=" + statePath}
	devScript := filepath.Join(root, "scripts", "dev-plugin.ps1")

	runPowerShell(t, environment, devScript,
		"-Action", "Install",
		"-RepositoryRoot", fixture,
		"-DoneThenPath", runtimePath,
		"-CodexCommand", fakeCodex,
		"-Apply",
	)

	stageRoot := filepath.Join(fixture, ".tmp", "done-then-dev-marketplace")
	stagePlugin := filepath.Join(stageRoot, "plugins", "done-then")
	var sourceManifest, stagedManifest struct {
		Version string `json:"version"`
	}
	readJSONFile(t, filepath.Join(fixture, ".codex-plugin", "plugin.json"), &sourceManifest)
	readJSONFile(t, filepath.Join(stagePlugin, ".codex-plugin", "plugin.json"), &stagedManifest)
	if sourceManifest.Version != "0.2.0" {
		t.Fatalf("source manifest version changed to %q", sourceManifest.Version)
	}
	if !strings.HasPrefix(stagedManifest.Version, "0.2.0+codex.local.") {
		t.Fatalf("staged cachebuster version = %q", stagedManifest.Version)
	}

	var mcp map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	readJSONFile(t, filepath.Join(stagePlugin, ".mcp.json"), &mcp)
	server := mcp["done_then"]
	launcherPath := ""
	for _, argument := range server.Args {
		base := filepath.Base(argument)
		if strings.HasPrefix(base, "invoke-donethen-") && strings.HasSuffix(base, ".ps1") {
			launcherPath = argument
			break
		}
	}
	if server.Command != "powershell.exe" || launcherPath == "" || !sameFile(launcherPath, filepath.Join(stagePlugin, "runtime", filepath.Base(launcherPath))) || !containsString(server.Args, "mcp") {
		t.Fatalf("staged MCP launcher = %#v", server)
	}
	launcher, err := os.ReadFile(launcherPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(string(launcher)), strings.ToLower(filepath.Base(runtimePath))) {
		t.Fatalf("staged launcher does not bind the reviewed runtime:\n%s", launcher)
	}

	var marketplace struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Source string `json:"source"`
				Path   string `json:"path"`
			} `json:"source"`
		} `json:"plugins"`
	}
	readJSONFile(t, filepath.Join(stageRoot, ".agents", "plugins", "marketplace.json"), &marketplace)
	if marketplace.Name != "done-then-dev" || len(marketplace.Plugins) != 1 || marketplace.Plugins[0].Name != "done-then" || marketplace.Plugins[0].Source.Source != "local" || marketplace.Plugins[0].Source.Path != "./plugins/done-then" {
		t.Fatalf("staged marketplace = %#v", marketplace)
	}
	receiptPath := filepath.Join(fixture, ".tmp", "done-then-dev-install.json")
	if _, err := os.Stat(receiptPath); err != nil {
		t.Fatalf("install receipt missing: %v", err)
	}
	var receipt struct {
		RuntimePath string `json:"runtime_path"`
	}
	readJSONFile(t, receiptPath, &receipt)
	if !sameFile(receipt.RuntimePath, runtimePath) {
		t.Fatalf("receipt runtime path %q does not identify %q", receipt.RuntimePath, runtimePath)
	}

	status := runPowerShell(t, environment, devScript,
		"-Action", "Status",
		"-RepositoryRoot", fixture,
		"-CodexCommand", fakeCodex,
	)
	if !strings.Contains(status, `"installed": true`) || !strings.Contains(status, `"script_owned_marketplace": true`) {
		t.Fatalf("unexpected installed status:\n%s", status)
	}

	runPowerShell(t, environment, devScript,
		"-Action", "Uninstall",
		"-RepositoryRoot", fixture,
		"-CodexCommand", fakeCodex,
		"-Apply",
	)
	if _, err := os.Stat(stageRoot); !os.IsNotExist(err) {
		t.Fatalf("uninstall retained script-owned stage: %v", err)
	}
	if _, err := os.Stat(runtimePath); err != nil {
		t.Fatalf("uninstall removed caller runtime: %v", err)
	}

	var fakeState struct {
		MarketplaceConfigured bool `json:"marketplaceConfigured"`
		Installed             bool `json:"installed"`
	}
	readJSONFile(t, statePath, &fakeState)
	if fakeState.MarketplaceConfigured || fakeState.Installed {
		t.Fatalf("fake Codex state after uninstall = %#v", fakeState)
	}
}

func TestLiveSmokeVerifiesObserveOnlyLifecycle(t *testing.T) {
	requireWindowsPowerShell(t)
	root := repositoryRoot(t)
	fixture := newPluginRepositoryFixture(t, root)
	dataRoot := filepath.Join(t.TempDir(), "DoneThen")
	statePath := filepath.Join(t.TempDir(), "fake-codex-state.json")
	fakeCodex := newFakeCodexCommand(t)
	otherPluginRoot := filepath.Join(t.TempDir(), "other plugin")
	if err := os.MkdirAll(filepath.Join(otherPluginRoot, ".codex-plugin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(otherPluginRoot, "hooks"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeJSONFile(t, filepath.Join(otherPluginRoot, ".codex-plugin", "plugin.json"), map[string]any{"name": "other", "version": "1.0.0"})
	otherHookPath := filepath.Join(otherPluginRoot, "hooks", "hooks.json")
	writeJSONFile(t, otherHookPath, map[string]any{"hooks": map[string]any{}})
	environment := []string{
		"DONETHEN_FAKE_CODEX_STATE=" + statePath,
		"DONETHEN_FAKE_OTHER_PLUGIN_ROOT=" + otherPluginRoot,
	}
	smokeScript := filepath.Join(root, "scripts", "live-smoke.ps1")

	runPowerShell(t, environment, smokeScript,
		"-Action", "Snapshot",
		"-RepositoryRoot", fixture,
		"-DataRoot", dataRoot,
		"-CodexCommand", fakeCodex,
		"-Apply",
	)
	runPowerShell(t, environment, smokeScript,
		"-Action", "Compare",
		"-RepositoryRoot", fixture,
		"-DataRoot", dataRoot,
		"-CodexCommand", fakeCodex,
	)
	runtimeData := []byte("@echo off\r\nexit /b 0\r\n")
	runtimePath := filepath.Join(t.TempDir(), "donethen-smoke.cmd")
	if err := os.WriteFile(runtimePath, runtimeData, 0o700); err != nil {
		t.Fatal(err)
	}
	runtimeDigest := sha256.Sum256(runtimeData)
	writeJSONFile(t, filepath.Join(fixture, ".tmp", "done-then-dev-install.json"), map[string]any{
		"schema_version": "1",
		"plugin_id":      "done-then@done-then-dev",
		"runtime_path":   runtimePath,
		"runtime_sha256": fmt.Sprintf("%x", runtimeDigest),
	})

	jobID := "dt_SMOKE123"
	jobDirectory := filepath.Join(dataRoot, "plugin", "jobs")
	eventDirectory := filepath.Join(dataRoot, "plugin", "events")
	if err := os.MkdirAll(jobDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(eventDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	job := map[string]any{
		"schema_version":                    "3",
		"job_id":                            jobID,
		"state":                             "DRY_RUN_COMPLETE",
		"reason_code":                       "after_stop_observed_no_action",
		"dry_run":                           true,
		"action":                            "shutdown",
		"trigger_policy":                    "after_stop",
		"stop_without_success_acknowledged": false,
		"verifier_profile":                  "none",
		"allow_agent_only_success":          false,
		"hook_compatibility":                "session_bound",
		"arm_observed":                      true,
		"finish_observed":                   false,
		"stop_turn_id":                      "turn-smoke",
	}
	writeJSONFile(t, filepath.Join(jobDirectory, jobID+".json"), job)

	eventNames := []string{"mcp.arm", "hook.post_tool.arm", "hook.stop"}
	var lines []string
	for index, name := range eventNames {
		event := map[string]any{
			"schema_version": "1",
			"timestamp":      fmt.Sprintf("2026-07-30T00:00:0%dZ", index),
			"job_id":         jobID,
			"name":           name,
			"event_key":      strings.Repeat("a", 64),
			"old_state":      "ARMED",
			"new_state":      "DRY_RUN_COMPLETE",
			"reason_code":    "after_stop_observed_no_action",
			"generation":     index + 1,
			"session_hash":   strings.Repeat("b", 64),
			"turn_hash":      strings.Repeat("c", 64),
		}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	if err := os.WriteFile(filepath.Join(eventDirectory, jobID+".jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	output := runPowerShell(t, environment, smokeScript,
		"-Action", "Verify",
		"-RepositoryRoot", fixture,
		"-DataRoot", dataRoot,
		"-CodexCommand", fakeCodex,
		"-JobId", jobID,
	)
	if !strings.Contains(output, `"ok": true`) || !strings.Contains(output, `"runtime_identity_verified": true`) || !strings.Contains(output, `"event_sequence_observed": true`) || !strings.Contains(output, `"power_action_event_count": 0`) {
		t.Fatalf("unexpected live-smoke result:\n%s", output)
	}

	if err := os.WriteFile(otherHookPath, []byte("{\"hooks\":{\"Stop\":[]}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedOutput, err := runPowerShellResult(environment, smokeScript,
		"-Action", "Compare",
		"-RepositoryRoot", fixture,
		"-DataRoot", dataRoot,
		"-CodexCommand", fakeCodex,
	)
	if err == nil || !strings.Contains(failedOutput, "configuration target content changed for plugin:other@fake:hook:hooks/hooks.json") {
		t.Fatalf("other-plugin Hook mutation did not fail closed: err=%v output=%s", err, failedOutput)
	}
}

func TestDevelopmentScriptsContainNoPowerOrTrustBypass(t *testing.T) {
	root := repositoryRoot(t)
	for _, name := range []string{"dev-plugin.ps1", "live-smoke.ps1"} {
		path := filepath.Join(root, "scripts", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.ToLower(string(data))
		for _, forbidden := range []string{"shutdown.exe", "dangerously-bypass-hook-trust", "set-content $home", "set-content $codex_home"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s contains forbidden automation %q", path, forbidden)
			}
		}
		if !strings.Contains(text, "supportsshouldprocess = $true") || !strings.Contains(text, "$apply") {
			t.Fatalf("%s is missing explicit mutation gates", path)
		}
	}
}

func newPluginRepositoryFixture(t *testing.T, root string) string {
	t.Helper()
	fixture := filepath.Join(t.TempDir(), "done-then fixture")
	if err := os.MkdirAll(fixture, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, item := range []string{".codex-plugin", "hooks", "skills", ".mcp.json", "README.md", "LICENSE", "CHANGELOG.md"} {
		copyTestPath(t, filepath.Join(root, item), filepath.Join(fixture, item))
	}
	return fixture
}

func copyTestPath(t *testing.T, source, destination string) {
	t.Helper()
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, data, info.Mode()); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := os.MkdirAll(destination, info.Mode()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		copyTestPath(t, filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name()))
	}
}

func newFakeCodexCommand(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	scriptPath := filepath.Join(directory, "fake-codex.ps1")
	script := `
[CmdletBinding()]
param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$CodexArguments
)

$ErrorActionPreference = 'Stop'
$statePath = $env:DONETHEN_FAKE_CODEX_STATE
if ([string]::IsNullOrWhiteSpace($statePath)) { throw 'missing fake state path' }
$state = [ordered]@{ marketplaceConfigured = $false; marketplaceRoot = ''; installed = $false }
if (Test-Path -LiteralPath $statePath) {
    $loaded = Get-Content -LiteralPath $statePath -Raw | ConvertFrom-Json
    $state.marketplaceConfigured = [bool]$loaded.marketplaceConfigured
    $state.marketplaceRoot = [string]$loaded.marketplaceRoot
    $state.installed = [bool]$loaded.installed
}

function Save-State {
    $json = $state | ConvertTo-Json
    $encoding = [System.Text.UTF8Encoding]::new($false)
    [System.IO.File]::WriteAllText($statePath, $json, $encoding)
}

if ($CodexArguments[0] -ne 'plugin') { throw 'unsupported fake command' }
if ($CodexArguments[1] -eq 'marketplace' -and $CodexArguments[2] -eq 'list') {
    $items = @()
    if ($state.marketplaceConfigured) {
        $items = @([ordered]@{ name = 'done-then-dev'; root = $state.marketplaceRoot })
    }
    [ordered]@{ marketplaces = $items } | ConvertTo-Json -Depth 8
    exit 0
}
if ($CodexArguments[1] -eq 'list') {
    $installed = @()
    $available = @()
    $otherPluginRoot = $env:DONETHEN_FAKE_OTHER_PLUGIN_ROOT
    if (-not [string]::IsNullOrWhiteSpace($otherPluginRoot)) {
        $installed += [ordered]@{
            pluginId = 'other@fake'
            name = 'other'
            marketplaceName = 'fake'
            source = [ordered]@{ source = 'local'; path = $otherPluginRoot }
        }
    }
    if ($state.marketplaceConfigured) {
        $plugin = [ordered]@{ pluginId = 'done-then@done-then-dev'; name = 'done-then'; marketplaceName = 'done-then-dev' }
        $available = @($plugin)
        if ($state.installed) { $installed = @($plugin) }
    }
    [ordered]@{ installed = $installed; available = $available } | ConvertTo-Json -Depth 8
    exit 0
}
if ($CodexArguments[1] -eq 'marketplace' -and $CodexArguments[2] -eq 'add') {
    $marketplacePath = Join-Path $CodexArguments[3] '.agents\plugins\marketplace.json'
    if (-not (Test-Path -LiteralPath $marketplacePath -PathType Leaf)) { throw 'staged marketplace is missing' }
    $state.marketplaceConfigured = $true
    $state.marketplaceRoot = [System.IO.Path]::GetFullPath($CodexArguments[3])
    Save-State
    '{"ok":true}'
    exit 0
}
if ($CodexArguments[1] -eq 'marketplace' -and $CodexArguments[2] -eq 'remove') {
    $state.marketplaceConfigured = $false
    $state.marketplaceRoot = ''
    Save-State
    '{"ok":true}'
    exit 0
}
if ($CodexArguments[1] -eq 'add') {
    if (-not $state.marketplaceConfigured) { throw 'marketplace is not configured' }
    $state.installed = $true
    Save-State
    '{"ok":true}'
    exit 0
}
if ($CodexArguments[1] -eq 'remove') {
    $state.installed = $false
    Save-State
    '{"ok":true}'
    exit 0
}
throw "unsupported fake command: $($CodexArguments -join ' ')"
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	commandPath := filepath.Join(directory, "fake-codex.cmd")
	command := "@echo off\r\npowershell.exe -NoLogo -NoProfile -NonInteractive -ExecutionPolicy Bypass -File \"%~dp0fake-codex.ps1\" %*\r\nexit /b %ERRORLEVEL%\r\n"
	if err := os.WriteFile(commandPath, []byte(command), 0o700); err != nil {
		t.Fatal(err)
	}
	return commandPath
}

func requireWindowsPowerShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "windows" {
		t.Skip("development PowerShell scripts target Windows")
	}
	if _, err := powerShellPath(); err != nil {
		t.Skipf("PowerShell unavailable: %v", err)
	}
}

func powerShellPath() (string, error) {
	if path, err := exec.LookPath("pwsh"); err == nil {
		return path, nil
	}
	return exec.LookPath("powershell.exe")
}

func runPowerShell(t *testing.T, environment []string, script string, arguments ...string) string {
	t.Helper()
	output, err := runPowerShellResult(environment, script, arguments...)
	if err != nil {
		t.Fatalf("PowerShell failed: %v\n%s", err, output)
	}
	return output
}

func runPowerShellResult(environment []string, script string, arguments ...string) (string, error) {
	powerShell, err := powerShellPath()
	if err != nil {
		return "", err
	}
	commandArguments := []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-File", script}
	commandArguments = append(commandArguments, arguments...)
	command := exec.Command(powerShell, commandArguments...)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	return string(output), err
}

func readJSONFile(t *testing.T, path string, destination any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func sameFile(left, right string) bool {
	leftInfo, leftErr := os.Stat(left)
	rightInfo, rightErr := os.Stat(right)
	return leftErr == nil && rightErr == nil && os.SameFile(leftInfo, rightInfo)
}
