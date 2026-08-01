package hostauthority

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	maxInventoryThreads = 256
	maxInventoryPages   = 16
)

type Adapter struct {
	client           Caller
	expectedPluginID string
	expectedHashes   map[string]string
	binding          AuthorityBinding
	now              func() time.Time
	events           *eventTracker
}

func NewAdapter(client Caller, expectedPluginID string, expectedHashes map[string]string) (*Adapter, error) {
	return NewAdapterWithBinding(client, expectedPluginID, expectedHashes, AuthorityBinding{Kind: BindingUnproven})
}

func NewAdapterWithBinding(client Caller, expectedPluginID string, expectedHashes map[string]string, binding AuthorityBinding) (*Adapter, error) {
	if client == nil {
		return nil, errors.New("app-server client is required")
	}
	if binding.Kind == "" {
		binding.Kind = BindingUnproven
	}
	if binding.Kind != BindingUnproven && binding.Kind != BindingSameHost {
		return nil, errors.New("unknown App Server authority binding")
	}
	if binding.Kind == BindingSameHost && binding.HostInstanceID == "" {
		return nil, errors.New("same-host App Server binding requires a host instance id")
	}
	copyHashes := make(map[string]string, len(expectedHashes))
	for key, value := range expectedHashes {
		copyHashes[key] = value
	}
	return &Adapter{client: client, expectedPluginID: expectedPluginID, expectedHashes: copyHashes, binding: binding, now: time.Now, events: newEventTracker()}, nil
}

func (a *Adapter) Snapshot(ctx context.Context, targetThreadID, cwd string) (Snapshot, error) {
	if targetThreadID == "" || cwd == "" {
		return Snapshot{}, errors.New("target thread id and cwd are required")
	}
	a.drainNotifications()
	snapshot := Snapshot{CapturedAt: a.now().UTC(), InventoryComplete: true, HostInstanceID: a.binding.HostInstanceID}
	loadedIDs, err := a.loadedThreadIDs(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("list loaded threads: %w", err)
	}
	if len(loadedIDs) > maxInventoryThreads {
		return Snapshot{}, fmt.Errorf("loaded thread inventory exceeds %d entries", maxInventoryThreads)
	}
	targetLoaded := false
	for _, threadID := range loadedIDs {
		thread, err := a.readThread(ctx, threadID, threadID == targetThreadID)
		if err != nil {
			snapshot.InventoryComplete = false
			snapshot.Reasons = append(snapshot.Reasons, "could not read every loaded thread")
			continue
		}
		if threadID == targetThreadID {
			if targetLoaded {
				snapshot.InventoryComplete = false
				snapshot.Reasons = append(snapshot.Reasons, "target thread appears more than once in loaded inventory")
			}
			targetLoaded = true
			snapshot.Target = thread
		}
		if !thread.Status.IsKnown() {
			snapshot.InventoryComplete = false
			snapshot.Reasons = append(snapshot.Reasons, "loaded thread has an unknown runtime status")
		}
		snapshot.LoadedThreads = append(snapshot.LoadedThreads, thread)
	}
	targetWorkspaceMatches := targetLoaded && WorkspacePathMatches(snapshot.Target.CWD, cwd)
	snapshot.SameHostProven = a.binding.SameHostVerified() && targetLoaded && targetWorkspaceMatches &&
		snapshot.Target.ID == targetThreadID && snapshot.Target.Status.IsKnown()
	if !snapshot.SameHostProven {
		if !a.binding.SameHostVerified() {
			snapshot.Reasons = append(snapshot.Reasons, "App Server connection has no verified same-host binding")
		} else if !targetWorkspaceMatches {
			snapshot.Reasons = append(snapshot.Reasons, "target thread workspace does not match the armed workspace")
		} else {
			snapshot.Reasons = append(snapshot.Reasons, "target thread is not loaded in the connected App Server")
		}
	}

	backgroundCount, err := a.backgroundTerminalCount(ctx, targetThreadID)
	if err != nil {
		snapshot.InventoryComplete = false
		snapshot.Reasons = append(snapshot.Reasons, "background terminal inventory is unavailable")
	} else {
		snapshot.BackgroundTerminalCount = backgroundCount
	}

	descendants, err := a.descendants(ctx, targetThreadID)
	if err != nil {
		snapshot.InventoryComplete = false
		snapshot.Reasons = append(snapshot.Reasons, "descendant thread inventory is unavailable")
	} else {
		snapshot.DescendantThreads = descendants
		for _, thread := range descendants {
			if thread.ID == "" || thread.ID == targetThreadID || !thread.Status.IsKnown() {
				snapshot.InventoryComplete = false
				snapshot.Reasons = append(snapshot.Reasons, "descendant thread inventory contains an invalid or unknown entry")
				break
			}
		}
	}

	var hooksResponse struct {
		Data []HookInventory `json:"data"`
	}
	if err := a.client.Call(ctx, "hooks/list", map[string]any{"cwds": []string{cwd}}, &hooksResponse); err != nil {
		return Snapshot{}, fmt.Errorf("list effective hooks: %w", err)
	}
	if len(hooksResponse.Data) != 1 {
		snapshot.InventoryComplete = false
		snapshot.Reasons = append(snapshot.Reasons, "hooks/list did not return exactly one cwd entry")
	} else {
		snapshot.Hooks = hooksResponse.Data[0]
		if !WorkspacePathMatches(snapshot.Hooks.CWD, cwd) {
			snapshot.InventoryComplete = false
			snapshot.Reasons = append(snapshot.Reasons, "hooks/list workspace does not match the armed workspace")
		}
		snapshot.HookDecision = EvaluateHooks(snapshot.Hooks, a.expectedPluginID, a.expectedHashes)
	}
	snapshot.EventLossDetected = a.client.EventLossDetected()
	a.drainNotifications()
	events := a.events.snapshot(targetThreadID)
	snapshot.LiveTargetObserved = events.liveTargetObserved
	snapshot.CompletedTurnIDs = events.completedTurnIDs
	snapshot.IncompleteHookCount = events.incompleteHookCount
	snapshot.HookFailureDetected = events.hookFailureDetected
	if events.decodeError {
		snapshot.EventLossDetected = true
	}
	if snapshot.EventLossDetected {
		snapshot.InventoryComplete = false
		snapshot.Reasons = append(snapshot.Reasons, "App Server notifications were dropped")
	}
	sort.Strings(snapshot.Reasons)
	return snapshot, nil
}

func (a *Adapter) drainNotifications() {
	channel := a.client.Notifications()
	for {
		select {
		case notification, ok := <-channel:
			if !ok {
				return
			}
			a.events.observe(notification)
		default:
			return
		}
	}
}

func (a *Adapter) loadedThreadIDs(ctx context.Context) ([]string, error) {
	ids := make([]string, 0)
	seenIDs := make(map[string]bool)
	seenCursors := make(map[string]bool)
	var cursor any
	for page := 0; page < maxInventoryPages; page++ {
		var response struct {
			Data       []string `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		if err := a.client.Call(ctx, "thread/loaded/list", map[string]any{"cursor": cursor, "limit": 100}, &response); err != nil {
			return nil, err
		}
		for _, id := range response.Data {
			if id == "" || seenIDs[id] {
				return nil, errors.New("loaded thread inventory contains an empty or duplicate id")
			}
			seenIDs[id] = true
			ids = append(ids, id)
		}
		if response.NextCursor == nil {
			return ids, nil
		}
		if *response.NextCursor == "" || seenCursors[*response.NextCursor] {
			return nil, errors.New("loaded thread inventory returned an invalid pagination cursor")
		}
		seenCursors[*response.NextCursor] = true
		cursor = *response.NextCursor
		if len(ids) > maxInventoryThreads {
			return nil, fmt.Errorf("thread inventory exceeds %d entries", maxInventoryThreads)
		}
	}
	return nil, fmt.Errorf("loaded thread inventory exceeds %d pages", maxInventoryPages)
}

func (a *Adapter) readThread(ctx context.Context, threadID string, includeTurns bool) (Thread, error) {
	var response struct {
		Thread Thread `json:"thread"`
	}
	if err := a.client.Call(ctx, "thread/read", map[string]any{"threadId": threadID, "includeTurns": includeTurns}, &response); err != nil {
		return Thread{}, err
	}
	if response.Thread.ID != threadID || response.Thread.Status.Type == "" {
		return Thread{}, errors.New("thread/read returned an incomplete thread")
	}
	return response.Thread, nil
}

func (a *Adapter) backgroundTerminalCount(ctx context.Context, threadID string) (int, error) {
	count := 0
	seenCursors := make(map[string]bool)
	var cursor any
	for page := 0; page < maxInventoryPages; page++ {
		var response struct {
			Data []struct {
				ProcessID string `json:"processId"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := a.client.Call(ctx, "thread/backgroundTerminals/list", map[string]any{"threadId": threadID, "cursor": cursor, "limit": 100}, &response); err != nil {
			return 0, err
		}
		count += len(response.Data)
		if count > maxInventoryThreads {
			return 0, fmt.Errorf("background terminal inventory exceeds %d entries", maxInventoryThreads)
		}
		if response.NextCursor == nil {
			return count, nil
		}
		if *response.NextCursor == "" || seenCursors[*response.NextCursor] {
			return 0, errors.New("background terminal inventory returned an invalid pagination cursor")
		}
		seenCursors[*response.NextCursor] = true
		cursor = *response.NextCursor
	}
	return 0, fmt.Errorf("background terminal inventory exceeds %d pages", maxInventoryPages)
}

func (a *Adapter) descendants(ctx context.Context, threadID string) ([]Thread, error) {
	threads := make([]Thread, 0)
	seenIDs := make(map[string]bool)
	seenCursors := make(map[string]bool)
	var cursor any
	for page := 0; page < maxInventoryPages; page++ {
		var response struct {
			Data       []Thread `json:"data"`
			NextCursor *string  `json:"nextCursor"`
		}
		params := map[string]any{
			"cursor":           cursor,
			"limit":            100,
			"ancestorThreadId": threadID,
			"sortKey":          "updated_at",
			"sortDirection":    "desc",
		}
		if err := a.client.Call(ctx, "thread/list", params, &response); err != nil {
			return nil, err
		}
		for _, thread := range response.Data {
			if thread.ID == "" || seenIDs[thread.ID] {
				return nil, errors.New("descendant inventory contains an empty or duplicate thread id")
			}
			seenIDs[thread.ID] = true
			threads = append(threads, thread)
		}
		if response.NextCursor == nil {
			return threads, nil
		}
		if *response.NextCursor == "" || seenCursors[*response.NextCursor] {
			return nil, errors.New("descendant inventory returned an invalid pagination cursor")
		}
		seenCursors[*response.NextCursor] = true
		cursor = *response.NextCursor
		if len(threads) > maxInventoryThreads {
			return nil, fmt.Errorf("descendant inventory exceeds %d entries", maxInventoryThreads)
		}
	}
	return nil, fmt.Errorf("descendant inventory exceeds %d pages", maxInventoryPages)
}
