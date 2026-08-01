package hostauthority

import (
	"encoding/json"
	"sort"
	"sync"
)

type eventTracker struct {
	mu             sync.Mutex
	seenThreads    map[string]bool
	completedTurns map[string]map[string]bool
	runningHooks   map[string]map[string]bool
	failedHooks    map[string]bool
	decodeError    bool
}

type targetEvents struct {
	liveTargetObserved  bool
	completedTurnIDs    []string
	incompleteHookCount int
	hookFailureDetected bool
	decodeError         bool
}

func newEventTracker() *eventTracker {
	return &eventTracker{
		seenThreads:    make(map[string]bool),
		completedTurns: make(map[string]map[string]bool),
		runningHooks:   make(map[string]map[string]bool),
		failedHooks:    make(map[string]bool),
	}
}

func (t *eventTracker) observe(notification Notification) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch notification.Method {
	case "thread/status/changed":
		var params struct {
			ThreadID string       `json:"threadId"`
			Status   ThreadStatus `json:"status"`
		}
		if json.Unmarshal(notification.Params, &params) != nil || params.ThreadID == "" || params.Status.Type == "" {
			t.decodeError = true
			return
		}
		t.seenThreads[params.ThreadID] = true
	case "turn/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Turn     Turn   `json:"turn"`
		}
		if json.Unmarshal(notification.Params, &params) != nil || params.ThreadID == "" || params.Turn.ID == "" || params.Turn.Status == "" {
			t.decodeError = true
			return
		}
		t.seenThreads[params.ThreadID] = true
		if params.Turn.Status == "completed" {
			if t.completedTurns[params.ThreadID] == nil {
				t.completedTurns[params.ThreadID] = make(map[string]bool)
			}
			t.completedTurns[params.ThreadID][params.Turn.ID] = true
		}
	case "hook/started", "hook/completed":
		var params struct {
			ThreadID string `json:"threadId"`
			Run      struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"run"`
		}
		if json.Unmarshal(notification.Params, &params) != nil || params.ThreadID == "" || params.Run.ID == "" || params.Run.Status == "" {
			t.decodeError = true
			return
		}
		t.seenThreads[params.ThreadID] = true
		if t.runningHooks[params.ThreadID] == nil {
			t.runningHooks[params.ThreadID] = make(map[string]bool)
		}
		if notification.Method == "hook/started" {
			t.runningHooks[params.ThreadID][params.Run.ID] = true
			return
		}
		delete(t.runningHooks[params.ThreadID], params.Run.ID)
		if params.Run.Status != "completed" {
			t.failedHooks[params.ThreadID] = true
		}
	}
}

func (t *eventTracker) snapshot(threadID string) targetEvents {
	t.mu.Lock()
	defer t.mu.Unlock()
	turnIDs := make([]string, 0, len(t.completedTurns[threadID]))
	for turnID := range t.completedTurns[threadID] {
		turnIDs = append(turnIDs, turnID)
	}
	sort.Strings(turnIDs)
	return targetEvents{
		liveTargetObserved:  t.seenThreads[threadID],
		completedTurnIDs:    turnIDs,
		incompleteHookCount: len(t.runningHooks[threadID]),
		hookFailureDetected: t.failedHooks[threadID],
		decodeError:         t.decodeError,
	}
}
