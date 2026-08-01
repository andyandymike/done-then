package hostauthority

import (
	"encoding/json"
	"testing"
)

func TestEventTrackerRequiresCompletedTurnAndSettledHooks(t *testing.T) {
	tracker := newEventTracker()
	tracker.observe(Notification{Method: "thread/status/changed", Params: json.RawMessage(`{"threadId":"thread","status":{"type":"active","activeFlags":[]}}`)})
	tracker.observe(Notification{Method: "hook/started", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","run":{"id":"hook-1","status":"running"}}`)})
	initial := tracker.snapshot("thread")
	if !initial.liveTargetObserved || initial.incompleteHookCount != 1 || len(initial.completedTurnIDs) != 0 {
		t.Fatalf("initial events = %#v", initial)
	}
	tracker.observe(Notification{Method: "hook/completed", Params: json.RawMessage(`{"threadId":"thread","turnId":"turn","run":{"id":"hook-1","status":"completed"}}`)})
	tracker.observe(Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread","turn":{"id":"turn","status":"completed"}}`)})
	settled := tracker.snapshot("thread")
	if settled.incompleteHookCount != 0 || settled.hookFailureDetected || len(settled.completedTurnIDs) != 1 || settled.completedTurnIDs[0] != "turn" {
		t.Fatalf("settled events = %#v", settled)
	}
}

func TestEventTrackerFailsClosedOnFailedHookOrMalformedAuthorityEvent(t *testing.T) {
	tracker := newEventTracker()
	tracker.observe(Notification{Method: "hook/completed", Params: json.RawMessage(`{"threadId":"thread","run":{"id":"hook-1","status":"failed"}}`)})
	tracker.observe(Notification{Method: "turn/completed", Params: json.RawMessage(`{"threadId":"thread"}`)})
	events := tracker.snapshot("thread")
	if !events.hookFailureDetected || !events.decodeError {
		t.Fatalf("failure events = %#v", events)
	}
}
