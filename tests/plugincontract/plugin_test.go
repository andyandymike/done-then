package plugincontract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPluginUsesOwnDefaultHookFileAndNarrowEvents(t *testing.T) {
	root := repositoryRoot(t)
	manifestData, err := os.ReadFile(filepath.Join(root, ".codex-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	if _, declared := manifest["hooks"]; declared {
		t.Fatal("manifest should use the plugin-owned default hooks/hooks.json discovery path")
	}
	if manifest["skills"] != "./skills/" || manifest["mcpServers"] != "./.mcp.json" {
		t.Fatalf("plugin component paths = %#v", manifest)
	}

	hookData, err := os.ReadFile(filepath.Join(root, "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type           string `json:"type"`
				Command        string `json:"command"`
				CommandWindows string `json:"commandWindows"`
				Timeout        int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(hookData, &document); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"PostToolUse": true, "UserPromptSubmit": true, "Stop": true, "SessionEnd": true}
	if len(document.Hooks) != len(allowed) {
		t.Fatalf("hook events = %#v", document.Hooks)
	}
	for event, groups := range document.Hooks {
		if !allowed[event] {
			t.Fatalf("broad or unsupported hook event %q", event)
		}
		for _, group := range groups {
			if event == "PostToolUse" && group.Matcher != "^mcp__done_then__(arm|finish|pause|cancel)$" {
				t.Fatalf("PostToolUse matcher = %q", group.Matcher)
			}
			for _, handler := range group.Hooks {
				if handler.Type != "command" || handler.Command != "donethen hook" || handler.CommandWindows != "donethen.exe hook" || handler.Timeout > 3 {
					t.Fatalf("unsafe hook handler = %#v", handler)
				}
			}
		}
	}
}

func TestPluginMCPLaunchesDoneThenWithoutShellArguments(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(repositoryRoot(t), ".mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var servers map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	if err := json.Unmarshal(data, &servers); err != nil {
		t.Fatal(err)
	}
	server, ok := servers["done_then"]
	if !ok || server.Command != "donethen" || len(server.Args) != 1 || server.Args[0] != "mcp" {
		t.Fatalf("done_then server = %#v", server)
	}
}

func TestPluginObserversCannotSchedulePowerDirectly(t *testing.T) {
	root := repositoryRoot(t)
	for _, directory := range []string{"pluginapi", "pluginstate", "mcpserver", "hookobserver"} {
		entries, err := os.ReadDir(filepath.Join(root, "internal", directory))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".go" || strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			path := filepath.Join(root, "internal", directory, entry.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			if strings.Contains(text, ".Schedule(") || strings.Contains(strings.ToLower(text), "shutdown.exe") {
				t.Fatalf("plugin runtime crossed the power-backend boundary in %s", path)
			}
		}
	}
}

func TestGitHubWorkflowsUsePinnedActionsAndNoPowerCommands(t *testing.T) {
	workflowRoot := filepath.Join(repositoryRoot(t), ".github", "workflows")
	entries, err := os.ReadDir(workflowRoot)
	if err != nil {
		t.Fatal(err)
	}
	actionReference := regexp.MustCompile(`(?m)^\s*uses:\s*([^#\s]+)`)
	commitSHA := regexp.MustCompile(`^[0-9a-f]{40}$`)
	for _, entry := range entries {
		if entry.IsDir() || (filepath.Ext(entry.Name()) != ".yml" && filepath.Ext(entry.Name()) != ".yaml") {
			continue
		}
		path := filepath.Join(workflowRoot, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		lower := strings.ToLower(text)
		for _, forbidden := range []string{"pull_request_target", "shutdown.exe", "stop-computer", "rundll32.exe"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("workflow %s contains forbidden content %q", path, forbidden)
			}
		}
		if !strings.Contains(text, "permissions:") {
			t.Fatalf("workflow %s has no explicit permissions block", path)
		}
		for _, match := range actionReference.FindAllStringSubmatch(text, -1) {
			reference := match[1]
			if strings.HasPrefix(reference, "./") || strings.HasPrefix(reference, "docker://") {
				continue
			}
			separator := strings.LastIndex(reference, "@")
			if separator < 0 || !commitSHA.MatchString(reference[separator+1:]) {
				t.Fatalf("workflow %s uses an unpinned action %q", path, reference)
			}
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(workingDirectory, "..", ".."))
}
