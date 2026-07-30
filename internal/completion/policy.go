package completion

import "fmt"

type Decision struct {
	Done   bool
	Reason string
}

func Evaluate(envelope Envelope) Decision {
	if envelope.SchemaVersion != "1" {
		return Decision{Reason: "unsupported completion schema"}
	}
	if envelope.Status != StatusDone {
		return Decision{Reason: fmt.Sprintf("Codex reported status=%s", envelope.Status)}
	}
	if envelope.ApprovalRequired {
		return Decision{Reason: "Codex reported that approval is required"}
	}
	if len(envelope.RemainingWork) != 0 {
		return Decision{Reason: "Codex reported remaining work"}
	}
	for _, check := range envelope.Checks {
		if check.Status != CheckPassed {
			return Decision{Reason: fmt.Sprintf("check %q reported status=%s", check.Name, check.Status)}
		}
	}
	return Decision{Done: true, Reason: "completion policy passed"}
}
