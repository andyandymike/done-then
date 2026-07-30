package completioncontract_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/andyandymike/done-then/internal/completion"
)

func TestParseValidEnvelope(t *testing.T) {
	data := mustJSON(t, validEnvelope())
	envelope, err := completion.Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if envelope.Status != completion.StatusDone || len(envelope.Checks) != 1 {
		t.Fatalf("Parse() = %#v", envelope)
	}
}

func TestCodexFacingSchemaUsesStructuredOutputsSubset(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(completion.SchemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	if _, exists := schema["$schema"]; exists {
		t.Fatal("Codex-facing schema contains unnecessary draft metadata")
	}
	for _, unsupported := range [][]byte{[]byte(`"minLength"`), []byte(`"maxLength"`)} {
		if bytes.Contains(completion.SchemaJSON, unsupported) {
			t.Fatalf("Codex-facing schema contains %s; length limits belong to independent validation", unsupported)
		}
	}
	if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
		t.Fatal("Codex-facing schema must set additionalProperties=false")
	}
}

func TestParseRejectsInvalidShapes(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{
			name: "empty",
			data: "",
			want: "empty",
		},
		{
			name: "unknown top-level field",
			data: `{"schema_version":"1","status":"done","summary":"ok","checks":[],"remaining_work":[],"approval_required":false,"extra":true}`,
			want: "unknown field",
		},
		{
			name: "missing false boolean",
			data: `{"schema_version":"1","status":"done","summary":"ok","checks":[],"remaining_work":[]}`,
			want: "missing a required field",
		},
		{
			name: "missing nested field",
			data: `{"schema_version":"1","status":"done","summary":"ok","checks":[{"name":"test","status":"passed"}],"remaining_work":[],"approval_required":false}`,
			want: "missing a required field",
		},
		{
			name: "invalid enum",
			data: `{"schema_version":"1","status":"complete","summary":"ok","checks":[],"remaining_work":[],"approval_required":false}`,
			want: "invalid completion status",
		},
		{
			name: "trailing value",
			data: `{"schema_version":"1","status":"done","summary":"ok","checks":[],"remaining_work":[],"approval_required":false} {}`,
			want: "trailing JSON value",
		},
		{
			name: "empty summary",
			data: `{"schema_version":"1","status":"done","summary":"","checks":[],"remaining_work":[],"approval_required":false}`,
			want: "summary must contain",
		},
		{
			name: "null checks",
			data: `{"schema_version":"1","status":"done","summary":"ok","checks":null,"remaining_work":[],"approval_required":false}`,
			want: "missing a required field",
		},
		{
			name: "duplicate top-level field",
			data: `{"schema_version":"1","status":"done","status":"partial","summary":"ok","checks":[],"remaining_work":[],"approval_required":false}`,
			want: "duplicate field",
		},
		{
			name: "duplicate nested field",
			data: `{"schema_version":"1","status":"done","summary":"ok","checks":[{"name":"a","name":"b","status":"passed","evidence":"ok"}],"remaining_work":[],"approval_required":false}`,
			want: "duplicate field",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := completion.Parse([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestParseRejectsOversizedResponse(t *testing.T) {
	data := make([]byte, completion.MaxResponseBytes+1)
	_, err := completion.Parse(data)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestEvaluateFailClosed(t *testing.T) {
	tests := []struct {
		name string
		edit func(*completion.Envelope)
		want string
	}{
		{name: "done", edit: func(*completion.Envelope) {}, want: ""},
		{name: "partial", edit: func(e *completion.Envelope) { e.Status = completion.StatusPartial }, want: "status=partial"},
		{name: "blocked", edit: func(e *completion.Envelope) { e.Status = completion.StatusBlocked }, want: "status=blocked"},
		{name: "failed", edit: func(e *completion.Envelope) { e.Status = completion.StatusFailed }, want: "status=failed"},
		{name: "approval", edit: func(e *completion.Envelope) { e.ApprovalRequired = true }, want: "approval"},
		{name: "remaining", edit: func(e *completion.Envelope) { e.RemainingWork = []string{"one item"} }, want: "remaining"},
		{name: "failed check", edit: func(e *completion.Envelope) { e.Checks[0].Status = completion.CheckFailed }, want: "status=failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := validEnvelope()
			test.edit(&envelope)
			decision := completion.Evaluate(envelope)
			if test.want == "" {
				if !decision.Done {
					t.Fatalf("Evaluate() = %#v", decision)
				}
				return
			}
			if decision.Done || !strings.Contains(decision.Reason, test.want) {
				t.Fatalf("Evaluate() = %#v, want reason containing %q", decision, test.want)
			}
		})
	}
}

func validEnvelope() completion.Envelope {
	return completion.Envelope{
		SchemaVersion: "1",
		Status:        completion.StatusDone,
		Summary:       "implemented and verified",
		Checks: []completion.Check{{
			Name:     "go test ./...",
			Status:   completion.CheckPassed,
			Evidence: "exit code 0",
		}},
		RemainingWork:    []string{},
		ApprovalRequired: false,
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
