package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andyandymike/done-then/internal/pluginapi"
	"github.com/andyandymike/done-then/internal/pluginstate"
)

func TestServerNegotiatesListsToolsAndRejectsExecute(t *testing.T) {
	state, err := pluginstate.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service, err := pluginapi.New(state)
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(service, "test")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test","version":"1"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"arm","arguments":{"action":"shutdown","trigger_policy":"after_stop","acknowledge_stop_without_success":true,"delay_seconds":120,"expires_in_seconds":3600,"mode":"execute","verifier_profile":"none","allow_agent_only_success":false}}}`,
	}, "\n")
	var output bytes.Buffer
	if err := server.Serve(context.Background(), strings.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var responses []map[string]any
	for decoder.More() {
		var response map[string]any
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %d, output=%q", len(responses), output.String())
	}
	initialize := responses[0]["result"].(map[string]any)
	if initialize["protocolVersion"] != "2025-11-25" {
		t.Fatalf("initialize = %#v", initialize)
	}
	listed := responses[1]["result"].(map[string]any)["tools"].([]any)
	if len(listed) != 5 {
		t.Fatalf("tools/list returned %d tools", len(listed))
	}
	call := responses[2]["result"].(map[string]any)
	if call["isError"] != true {
		t.Fatalf("execute call did not fail closed: %#v", call)
	}
	structured := call["structuredContent"].(map[string]any)
	if structured["reason_code"] != "execute_unavailable" || structured["power_action_called"] != false {
		t.Fatalf("execute result = %#v", structured)
	}
	jobs, err := state.List()
	if err != nil || len(jobs) != 0 {
		t.Fatalf("execute rejection jobs = %#v, %v", jobs, err)
	}
}

func TestArmSchemaPublishesMultiSessionBarrierInputs(t *testing.T) {
	listed := tools()
	arm := listed[0]
	properties := arm.InputSchema["properties"].(map[string]any)
	trigger := properties["trigger_policy"].(map[string]any)
	enum := trigger["enum"].([]string)
	found := false
	for _, value := range enum {
		if value == "after_all_stop" {
			found = true
		}
	}
	if !found || properties["target_session_ids"] == nil || properties["acknowledge_barrier_across_turns"] == nil {
		t.Fatalf("arm barrier schema = %#v", arm.InputSchema)
	}
}
