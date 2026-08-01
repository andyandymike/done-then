package hostauthority

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestClientInitializeCallAndNotification(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()
	client := NewClient(clientSide, clientSide)
	serverDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(serverSide)
		encoder := json.NewEncoder(serverSide)
		if !scanner.Scan() {
			serverDone <- scanner.Err()
			return
		}
		var initialize map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &initialize); err != nil {
			serverDone <- err
			return
		}
		if err := encoder.Encode(map[string]any{"id": initialize["id"], "result": map[string]any{"serverInfo": map[string]any{"name": "codex"}}}); err != nil {
			serverDone <- err
			return
		}
		if !scanner.Scan() {
			serverDone <- scanner.Err()
			return
		}
		if err := encoder.Encode(map[string]any{"method": "thread/status/changed", "params": map[string]any{"threadId": "thr_1", "status": map[string]any{"type": "idle"}}}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Initialize(ctx, "done_then_test", "test", true); err != nil {
		t.Fatal(err)
	}
	select {
	case notification := <-client.Notifications():
		if notification.Method != "thread/status/changed" {
			t.Fatalf("notification = %#v", notification)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
