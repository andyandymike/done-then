package supervisor

import "fmt"

var allowedTransitions = map[State]map[State]struct{}{
	StateArmed: {
		StateAgentRunning: {},
		StateCancelled:    {},
		StateOrphaned:     {},
	},
	StateAgentRunning: {
		StateEvaluating:  {},
		StateAgentFailed: {},
		StateCancelled:   {},
		StateOrphaned:    {},
	},
	StateEvaluating: {
		StateVerifying:            {},
		StateActionIntentRecorded: {},
		StateDryRunComplete:       {},
		StateNotDone:              {},
		StateInvalidCompletion:    {},
		StateCancelled:            {},
		StateOrphaned:             {},
	},
	StateVerifying: {
		StateActionIntentRecorded: {},
		StateDryRunComplete:       {},
		StateVerificationFailed:   {},
		StateCancelled:            {},
		StateOrphaned:             {},
	},
	StateActionIntentRecorded: {
		StateActionScheduled: {},
		StateActionFailed:    {},
		StateCancelled:       {},
	},
	StateActionScheduled: {
		StateCancelled: {},
	},
}

func Transition(from, to State) error {
	if next, ok := allowedTransitions[from]; ok {
		if _, ok := next[to]; ok {
			return nil
		}
	}
	return fmt.Errorf("invalid job transition %s -> %s", from, to)
}
