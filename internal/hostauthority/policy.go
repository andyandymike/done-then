package hostauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var doneThenToolNames = []string{
	"mcp__done_then__arm",
	"mcp__done_then__finish",
	"mcp__done_then__pause",
	"mcp__done_then__cancel",
	"mcp__done_then__status",
}

func EvaluateHooks(inventory HookInventory, expectedPluginID string, expectedHashes map[string]string) HookDecision {
	reasons := make([]string, 0)
	expectedCounts := make(map[string]int, len(expectedHashes))
	if strings.TrimSpace(inventory.CWD) == "" {
		reasons = append(reasons, "hook inventory has no cwd")
	}
	if len(inventory.Errors) != 0 {
		reasons = append(reasons, "hook inventory contains discovery errors")
	}
	if len(inventory.Warnings) != 0 {
		reasons = append(reasons, "hook inventory contains warnings")
	}
	foundExpected := 0
	for _, hook := range inventory.Hooks {
		if !hook.Enabled {
			continue
		}
		isDoneThen := hook.PluginID != nil && *hook.PluginID == expectedPluginID
		if isDoneThen {
			foundExpected++
			if !trustedHookStatus(hook.TrustStatus) {
				reasons = append(reasons, fmt.Sprintf("DoneThen hook %s is not trusted", safeHookKey(hook.Key)))
			}
			if hook.CurrentHash == "" {
				reasons = append(reasons, fmt.Sprintf("DoneThen hook %s has no definition hash", safeHookKey(hook.Key)))
			}
			expected, ok := expectedHashes[hook.Key]
			if !ok {
				reasons = append(reasons, fmt.Sprintf("DoneThen hook %s is not present in the installed policy", safeHookKey(hook.Key)))
			} else if expected != hook.CurrentHash {
				reasons = append(reasons, fmt.Sprintf("DoneThen hook %s hash changed", safeHookKey(hook.Key)))
			}
			expectedCounts[hook.Key]++
			continue
		}
		if unknownHookSource(hook.Source) || !trustedHookStatus(hook.TrustStatus) {
			if hookRelevant(hook) {
				reasons = append(reasons, fmt.Sprintf("relevant hook %s has unknown, modified, or untrusted provenance", safeHookKey(hook.Key)))
			}
			continue
		}
		if canonicalEventName(hook.EventName) == "stop" {
			reasons = append(reasons, fmt.Sprintf("non-DoneThen Stop hook %s is enabled", safeHookKey(hook.Key)))
			continue
		}
		if toolHookIntersects(hook) {
			reasons = append(reasons, fmt.Sprintf("hook %s can match a DoneThen MCP tool", safeHookKey(hook.Key)))
		}
	}
	if expectedPluginID == "" {
		reasons = append(reasons, "expected DoneThen plugin id is unavailable")
	} else if len(expectedHashes) == 0 {
		reasons = append(reasons, "expected DoneThen hook hash policy is unavailable")
	} else if foundExpected == 0 {
		reasons = append(reasons, "DoneThen hooks are absent from the effective inventory")
	}
	if len(expectedHashes) != 0 {
		for key := range expectedHashes {
			if expectedCounts[key] == 0 {
				reasons = append(reasons, fmt.Sprintf("expected DoneThen hook %s is missing", safeHookKey(key)))
			} else if expectedCounts[key] != 1 {
				reasons = append(reasons, fmt.Sprintf("expected DoneThen hook %s appears more than once", safeHookKey(key)))
			}
		}
	}
	sort.Strings(reasons)
	return HookDecision{
		Compatible:  len(reasons) == 0,
		Fingerprint: hookFingerprint(inventory),
		Reasons:     reasons,
	}
}

func hookRelevant(hook Hook) bool {
	switch canonicalEventName(hook.EventName) {
	case "stop", "pretooluse", "permissionrequest", "posttooluse":
		return true
	default:
		return false
	}
}

func toolHookIntersects(hook Hook) bool {
	eventName := canonicalEventName(hook.EventName)
	if eventName != "pretooluse" && eventName != "permissionrequest" && eventName != "posttooluse" {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(hook.HandlerType), "command") {
		return true
	}
	if hook.Matcher == nil || strings.TrimSpace(*hook.Matcher) == "" {
		return true
	}
	expression, err := regexp.Compile(*hook.Matcher)
	if err != nil {
		return true
	}
	for _, toolName := range doneThenToolNames {
		if expression.MatchString(toolName) {
			return true
		}
	}
	return false
}

func canonicalEventName(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func trustedHookStatus(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "trusted" || value == "managed"
}

func unknownHookSource(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "" || value == "unknown"
}

func hookFingerprint(inventory HookInventory) string {
	type fingerprintHook struct {
		CurrentHash string  `json:"current_hash"`
		Enabled     bool    `json:"enabled"`
		EventName   string  `json:"event_name"`
		HandlerType string  `json:"handler_type"`
		IsManaged   bool    `json:"is_managed"`
		Key         string  `json:"key"`
		Matcher     *string `json:"matcher"`
		PluginID    *string `json:"plugin_id"`
		Source      string  `json:"source"`
		SourcePath  string  `json:"source_path"`
		TimeoutSec  uint64  `json:"timeout_sec"`
		TrustStatus string  `json:"trust_status"`
	}
	hooks := make([]fingerprintHook, 0, len(inventory.Hooks))
	for _, hook := range inventory.Hooks {
		hooks = append(hooks, fingerprintHook{
			CurrentHash: hook.CurrentHash,
			Enabled:     hook.Enabled,
			EventName:   hook.EventName,
			HandlerType: hook.HandlerType,
			IsManaged:   hook.IsManaged,
			Key:         hook.Key,
			Matcher:     hook.Matcher,
			PluginID:    hook.PluginID,
			Source:      hook.Source,
			SourcePath:  hook.SourcePath,
			TimeoutSec:  hook.TimeoutSec,
			TrustStatus: hook.TrustStatus,
		})
	}
	sort.Slice(hooks, func(i, j int) bool {
		left, _ := json.Marshal(hooks[i])
		right, _ := json.Marshal(hooks[j])
		return string(left) < string(right)
	})
	payload, _ := json.Marshal(struct {
		CWD      string            `json:"cwd"`
		Errors   []HookError       `json:"errors"`
		Hooks    []fingerprintHook `json:"hooks"`
		Warnings []string          `json:"warnings"`
	}{inventory.CWD, inventory.Errors, hooks, inventory.Warnings})
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func safeHookKey(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "<unknown>"
	}
	if len(value) > 48 {
		return value[:48]
	}
	return value
}
