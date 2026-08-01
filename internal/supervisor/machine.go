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
		StateActionFailed:         {},
		StateDryRunComplete:       {},
		StateNotDone:              {},
		StateInvalidCompletion:    {},
		StateCancelled:            {},
		StateOrphaned:             {},
	},
	StateVerifying: {
		StateActionIntentRecorded: {},
		StateActionFailed:         {},
		StateDryRunComplete:       {},
		StateVerificationFailed:   {},
		StateCancelled:            {},
		StateOrphaned:             {},
	},
	StateActionIntentRecorded: {
		StateActionScheduled:           {},
		StateActionFailed:              {},
		StateCancelled:                 {},
		StateActionExecutionUnverified: {},
		StateActionExecutedConfirmed:   {},
	},
	StateActionScheduled: {
		StateCancelled:                 {},
		StateActionExecutionUnverified: {},
		StateActionExecutedConfirmed:   {},
	},
	StateActionExecutionUnverified: {
		StateActionExecutedConfirmed: {},
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
