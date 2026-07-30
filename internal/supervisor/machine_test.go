package supervisor

import "testing"

func TestTransitionMatrixIsExact(t *testing.T) {
	allowed := [][2]State{
		{StateArmed, StateAgentRunning},
		{StateArmed, StateCancelled},
		{StateArmed, StateOrphaned},
		{StateAgentRunning, StateEvaluating},
		{StateAgentRunning, StateAgentFailed},
		{StateAgentRunning, StateCancelled},
		{StateAgentRunning, StateOrphaned},
		{StateEvaluating, StateVerifying},
		{StateEvaluating, StateActionIntentRecorded},
		{StateEvaluating, StateDryRunComplete},
		{StateEvaluating, StateNotDone},
		{StateEvaluating, StateInvalidCompletion},
		{StateEvaluating, StateCancelled},
		{StateEvaluating, StateOrphaned},
		{StateVerifying, StateActionIntentRecorded},
		{StateVerifying, StateDryRunComplete},
		{StateVerifying, StateVerificationFailed},
		{StateVerifying, StateCancelled},
		{StateVerifying, StateOrphaned},
		{StateActionIntentRecorded, StateActionScheduled},
		{StateActionIntentRecorded, StateActionFailed},
		{StateActionIntentRecorded, StateCancelled},
		{StateActionScheduled, StateCancelled},
	}
	expected := make(map[[2]State]bool, len(allowed))
	for _, pair := range allowed {
		expected[pair] = true
	}
	states := []State{
		StateArmed,
		StateAgentRunning,
		StateEvaluating,
		StateVerifying,
		StateActionIntentRecorded,
		StateActionScheduled,
		StateDryRunComplete,
		StateNotDone,
		StateAgentFailed,
		StateInvalidCompletion,
		StateVerificationFailed,
		StateActionFailed,
		StateCancelled,
		StateOrphaned,
	}
	for _, from := range states {
		for _, to := range states {
			err := Transition(from, to)
			if expected[[2]State{from, to}] && err != nil {
				t.Errorf("Transition(%s, %s) error = %v", from, to, err)
			}
			if !expected[[2]State{from, to}] && err == nil {
				t.Errorf("Transition(%s, %s) unexpectedly succeeded", from, to)
			}
		}
	}
}

func TestStateClassifications(t *testing.T) {
	if !StateCancelled.IsTerminal() || StateActionScheduled.IsTerminal() {
		t.Fatal("terminal state classification is incorrect")
	}
	if !StateActionScheduled.IsUnresolvedPowerState() || !StateActionIntentRecorded.IsUnresolvedPowerState() {
		t.Fatal("unresolved power state classification is incorrect")
	}
}
