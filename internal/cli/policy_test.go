package cli

import (
	"testing"

	"github.com/andyandymike/done-then/internal/hostauthority"
)

func TestCaptureDoneThenHashesRequiresCompletePluginHookSet(t *testing.T) {
	pluginID := "done-then"
	events := []string{"PostToolUse", "UserPromptSubmit", "Stop", "SessionEnd"}
	hooks := make([]hostauthority.Hook, 0, len(events))
	for _, event := range events {
		hooks = append(hooks, hostauthority.Hook{
			Enabled: true, EventName: event, Key: "done-" + event,
			CurrentHash: "sha256:" + event, PluginID: &pluginID,
		})
	}
	hashes, err := captureDoneThenHashes(hostauthority.HookInventory{Hooks: hooks}, pluginID)
	if err != nil || len(hashes) != 4 {
		t.Fatalf("hashes=%#v err=%v", hashes, err)
	}
	hooks = hooks[:3]
	if _, err := captureDoneThenHashes(hostauthority.HookInventory{Hooks: hooks}, pluginID); err == nil {
		t.Fatal("incomplete DoneThen Hook set was accepted")
	}
}
