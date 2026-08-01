package hostauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

type fakeCaller struct {
	handlers      map[string]func(any) (any, error)
	eventLoss     bool
	notifications chan Notification
}

func (f *fakeCaller) Call(_ context.Context, method string, params any, destination any) error {
	handler := f.handlers[method]
	if handler == nil {
		return fmt.Errorf("unexpected method %s", method)
	}
	result, err := handler(params)
	if err != nil {
		return err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, destination)
}

func (*fakeCaller) Notify(string, any) error             { return nil }
func (f *fakeCaller) Notifications() <-chan Notification { return f.notifications }
func (f *fakeCaller) EventLossDetected() bool            { return f.eventLoss }

func TestAdapterSnapshotProvesLoadedIdleTarget(t *testing.T) {
	pluginID := "done-then"
	workspace := filepath.Join(t.TempDir(), "repo")
	client := &fakeCaller{notifications: make(chan Notification, 2), handlers: map[string]func(any) (any, error){
		"thread/loaded/list": func(any) (any, error) {
			return map[string]any{"data": []string{"thr_target"}, "nextCursor": nil}, nil
		},
		"thread/read": func(any) (any, error) {
			return map[string]any{"thread": Thread{
				ID: "thr_target", CWD: workspace, Status: ThreadStatus{Type: "idle"},
				Turns: []Turn{{ID: "turn_done", Status: "completed"}},
			}}, nil
		},
		"thread/backgroundTerminals/list": func(any) (any, error) {
			return map[string]any{"data": []any{}, "nextCursor": nil}, nil
		},
		"thread/list": func(any) (any, error) {
			return map[string]any{"data": []Thread{}, "nextCursor": nil}, nil
		},
		"hooks/list": func(any) (any, error) {
			return map[string]any{"data": []HookInventory{{
				CWD: workspace,
				Hooks: []Hook{{
					CurrentHash: "hash", Enabled: true, EventName: "stop", HandlerType: "command",
					Key: "done-stop", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted",
				}},
			}}}, nil
		},
	}}
	client.notifications <- Notification{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thr_target","status":{"type":"idle"}}`)}
	client.notifications <- Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thr_target","turn":{"id":"turn_done","status":"completed"}}`)}
	adapter, err := NewAdapterWithBinding(client, pluginID, map[string]string{"done-stop": "hash"}, AuthorityBinding{
		Kind: BindingSameHost, HostInstanceID: "host-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter.now = func() time.Time { return time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC) }
	snapshot, err := adapter.Snapshot(context.Background(), "thr_target", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.ReadyForFinalGate("turn_done") {
		t.Fatalf("snapshot = %#v", snapshot)
	}
	if snapshot.HostInstanceID != "host-test" {
		t.Fatalf("host instance = %q", snapshot.HostInstanceID)
	}
}

func TestAdapterRejectsIsolatedConnectionAsSameHostAuthority(t *testing.T) {
	pluginID := "done-then"
	workspace := filepath.Join(t.TempDir(), "repo")
	client := &fakeCaller{notifications: make(chan Notification, 2), handlers: map[string]func(any) (any, error){
		"thread/loaded/list": func(any) (any, error) {
			return map[string]any{"data": []string{"thr_target"}, "nextCursor": nil}, nil
		},
		"thread/read": func(any) (any, error) {
			return map[string]any{"thread": Thread{
				ID: "thr_target", CWD: workspace, Status: ThreadStatus{Type: "idle"},
				Turns: []Turn{{ID: "turn_done", Status: "completed"}},
			}}, nil
		},
		"thread/backgroundTerminals/list": func(any) (any, error) {
			return map[string]any{"data": []any{}, "nextCursor": nil}, nil
		},
		"thread/list": func(any) (any, error) {
			return map[string]any{"data": []Thread{}, "nextCursor": nil}, nil
		},
		"hooks/list": func(any) (any, error) {
			return map[string]any{"data": []HookInventory{{
				CWD: workspace, Hooks: []Hook{{CurrentHash: "hash", Enabled: true, EventName: "stop", HandlerType: "command", Key: "done-stop", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted"}},
			}}}, nil
		},
	}}
	client.notifications <- Notification{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thr_target","status":{"type":"idle"}}`)}
	client.notifications <- Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thr_target","turn":{"id":"turn_done","status":"completed"}}`)}
	adapter, err := NewAdapter(client, pluginID, map[string]string{"done-stop": "hash"})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := adapter.Snapshot(context.Background(), "thr_target", workspace)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.SameHostProven || snapshot.ReadyForFinalGate("turn_done") {
		t.Fatalf("isolated App Server was accepted as same-host authority: %#v", snapshot)
	}
}

func TestAdapterSnapshotFailsClosedWhenExperimentalInventoryIsUnavailable(t *testing.T) {
	pluginID := "done-then"
	client := &fakeCaller{handlers: map[string]func(any) (any, error){
		"thread/loaded/list": func(any) (any, error) { return map[string]any{"data": []string{"thr_target"}}, nil },
		"thread/read": func(any) (any, error) {
			return map[string]any{"thread": Thread{ID: "thr_target", Status: ThreadStatus{Type: "idle"}, Turns: []Turn{{ID: "turn_done", Status: "completed"}}}}, nil
		},
		"thread/backgroundTerminals/list": func(any) (any, error) { return nil, errors.New("unsupported") },
		"thread/list":                     func(any) (any, error) { return nil, errors.New("unsupported") },
		"hooks/list": func(any) (any, error) {
			return map[string]any{"data": []HookInventory{{CWD: "C:/repo", Hooks: []Hook{{CurrentHash: "hash", Enabled: true, EventName: "stop", HandlerType: "command", Key: "done", PluginID: &pluginID, Source: "plugin", TrustStatus: "trusted"}}}}}, nil
		},
	}}
	adapter, _ := NewAdapter(client, pluginID, nil)
	snapshot, err := adapter.Snapshot(context.Background(), "thr_target", "C:/repo")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.InventoryComplete || snapshot.ReadyForFinalGate("turn_done") {
		t.Fatalf("snapshot should fail closed: %#v", snapshot)
	}
}
