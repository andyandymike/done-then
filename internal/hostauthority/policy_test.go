package hostauthority

import "testing"

func TestEvaluateHooksAcceptsTrustedDoneThenAndUnrelatedHook(t *testing.T) {
	pluginID := "done-then"
	otherID := "logger"
	unrelatedMatcher := "^mcp__filesystem__read_file$"
	inventory := HookInventory{
		CWD: "C:/repo",
		Hooks: []Hook{
			{
				CurrentHash: "done-hash", Enabled: true, EventName: "stop", HandlerType: "command",
				Key: "done-stop", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted",
			},
			{
				CurrentHash: "other-hash", Enabled: true, EventName: "postToolUse", HandlerType: "command",
				Key: "other-read", Matcher: &unrelatedMatcher, PluginID: &otherID, Source: "plugin", TrustStatus: "trusted",
			},
		},
	}
	decision := EvaluateHooks(inventory, pluginID, map[string]string{"done-stop": "done-hash"})
	if !decision.Compatible || decision.Fingerprint == "" || len(decision.Reasons) != 0 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestEvaluateHooksRejectsStopAndIntersectingToolHooks(t *testing.T) {
	pluginID := "done-then"
	otherID := "other"
	broad := ".*"
	inventory := HookInventory{
		CWD: "C:/repo",
		Hooks: []Hook{
			{CurrentHash: "done", Enabled: true, EventName: "stop", HandlerType: "command", Key: "done", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted"},
			{CurrentHash: "stop", Enabled: true, EventName: "stop", HandlerType: "command", Key: "other-stop", PluginID: &otherID, Source: "plugin", TrustStatus: "trusted"},
			{CurrentHash: "tool", Enabled: true, EventName: "postToolUse", HandlerType: "command", Key: "broad", Matcher: &broad, PluginID: &otherID, Source: "plugin", TrustStatus: "trusted"},
		},
	}
	decision := EvaluateHooks(inventory, pluginID, map[string]string{"done": "done"})
	if decision.Compatible || len(decision.Reasons) != 2 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestEvaluateHooksRejectsCaseVariantStopUntrustedAndUnexpectedDoneThenHooks(t *testing.T) {
	pluginID := "done-then"
	otherID := "other"
	inventory := HookInventory{
		CWD: "C:/repo",
		Hooks: []Hook{
			{CurrentHash: "expected", Enabled: true, EventName: "Stop", HandlerType: "command", Key: "done", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted"},
			{CurrentHash: "extra", Enabled: true, EventName: "PostToolUse", HandlerType: "command", Key: "extra", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted"},
			{CurrentHash: "foreign", Enabled: true, EventName: "STOP", HandlerType: "command", Key: "foreign", PluginID: &otherID, Source: "plugin", TrustStatus: "trusted"},
			{CurrentHash: "untrusted", Enabled: true, EventName: "PostToolUse", HandlerType: "command", Key: "untrusted", PluginID: &otherID, Source: "plugin", TrustStatus: "untrusted"},
		},
	}
	decision := EvaluateHooks(inventory, pluginID, map[string]string{"done": "expected"})
	if decision.Compatible || len(decision.Reasons) != 3 {
		t.Fatalf("decision = %#v", decision)
	}
}

func TestEvaluateHooksRejectsDuplicateExpectedHook(t *testing.T) {
	pluginID := "done-then"
	inventory := HookInventory{CWD: "C:/repo", Hooks: []Hook{
		{CurrentHash: "hash", Enabled: true, EventName: "Stop", HandlerType: "command", Key: "done", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted"},
		{CurrentHash: "hash", Enabled: true, EventName: "Stop", HandlerType: "command", Key: "done", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted"},
	}}
	decision := EvaluateHooks(inventory, pluginID, map[string]string{"done": "hash"})
	if decision.Compatible {
		t.Fatalf("duplicate expected hook was accepted: %#v", decision)
	}
}

func TestHookFingerprintChangesWithPolicyRelevantFields(t *testing.T) {
	pluginID := "done-then"
	inventory := HookInventory{CWD: "C:/repo", Hooks: []Hook{{
		CurrentHash: "one", Enabled: true, EventName: "stop", HandlerType: "command",
		Key: "done", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted",
	}}}
	first := EvaluateHooks(inventory, pluginID, nil).Fingerprint
	inventory.Hooks[0].CurrentHash = "two"
	second := EvaluateHooks(inventory, pluginID, nil).Fingerprint
	if first == second {
		t.Fatal("hook fingerprint did not change")
	}
}
