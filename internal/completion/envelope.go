package completion

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	MaxResponseBytes = 1 << 20
	MaxSummaryRunes  = 4096
	MaxChecks        = 64
	MaxCheckName     = 256
	MaxEvidence      = 4096
	MaxRemainingWork = 64
	MaxRemainingItem = 1024
)

//go:embed schema.json
var SchemaJSON []byte

type Status string

const (
	StatusDone    Status = "done"
	StatusPartial Status = "partial"
	StatusBlocked Status = "blocked"
	StatusFailed  Status = "failed"
)

type CheckStatus string

const (
	CheckPassed CheckStatus = "passed"
	CheckFailed CheckStatus = "failed"
	CheckNotRun CheckStatus = "not_run"
)

type Check struct {
	Name     string      `json:"name"`
	Status   CheckStatus `json:"status"`
	Evidence string      `json:"evidence"`
}

type Envelope struct {
	SchemaVersion    string   `json:"schema_version"`
	Status           Status   `json:"status"`
	Summary          string   `json:"summary"`
	Checks           []Check  `json:"checks"`
	RemainingWork    []string `json:"remaining_work"`
	ApprovalRequired bool     `json:"approval_required"`
}

type wireEnvelope struct {
	SchemaVersion    *string      `json:"schema_version"`
	Status           *Status      `json:"status"`
	Summary          *string      `json:"summary"`
	Checks           *[]wireCheck `json:"checks"`
	RemainingWork    *[]string    `json:"remaining_work"`
	ApprovalRequired *bool        `json:"approval_required"`
}

type wireCheck struct {
	Name     *string      `json:"name"`
	Status   *CheckStatus `json:"status"`
	Evidence *string      `json:"evidence"`
}

func Parse(data []byte) (Envelope, error) {
	if len(data) == 0 {
		return Envelope{}, errors.New("completion envelope is empty")
	}
	if len(data) > MaxResponseBytes {
		return Envelope{}, fmt.Errorf("completion envelope exceeds %d bytes", MaxResponseBytes)
	}
	if !utf8.Valid(data) {
		return Envelope{}, errors.New("completion envelope is not valid UTF-8")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return Envelope{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire wireEnvelope
	if err := decoder.Decode(&wire); err != nil {
		return Envelope{}, fmt.Errorf("decode completion envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Envelope{}, errors.New("completion envelope contains a trailing JSON value")
		}
		return Envelope{}, fmt.Errorf("decode trailing completion data: %w", err)
	}

	if wire.SchemaVersion == nil || wire.Status == nil || wire.Summary == nil ||
		wire.Checks == nil || wire.RemainingWork == nil || wire.ApprovalRequired == nil {
		return Envelope{}, errors.New("completion envelope is missing a required field")
	}

	envelope := Envelope{
		SchemaVersion:    *wire.SchemaVersion,
		Status:           *wire.Status,
		Summary:          *wire.Summary,
		RemainingWork:    append([]string(nil), (*wire.RemainingWork)...),
		ApprovalRequired: *wire.ApprovalRequired,
		Checks:           make([]Check, 0, len(*wire.Checks)),
	}
	for index, item := range *wire.Checks {
		if item.Name == nil || item.Status == nil || item.Evidence == nil {
			return Envelope{}, fmt.Errorf("check %d is missing a required field", index)
		}
		envelope.Checks = append(envelope.Checks, Check{
			Name:     *item.Name,
			Status:   *item.Status,
			Evidence: *item.Evidence,
		})
	}

	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (e Envelope) Validate() error {
	if e.SchemaVersion != "1" {
		return fmt.Errorf("unsupported schema_version %q", e.SchemaVersion)
	}
	switch e.Status {
	case StatusDone, StatusPartial, StatusBlocked, StatusFailed:
	default:
		return fmt.Errorf("invalid completion status %q", e.Status)
	}
	if runeLength(e.Summary) < 1 || runeLength(e.Summary) > MaxSummaryRunes {
		return fmt.Errorf("summary must contain between 1 and %d characters", MaxSummaryRunes)
	}
	if len(e.Checks) > MaxChecks {
		return fmt.Errorf("checks exceeds %d items", MaxChecks)
	}
	for index, check := range e.Checks {
		if runeLength(check.Name) < 1 || runeLength(check.Name) > MaxCheckName {
			return fmt.Errorf("check %d name must contain between 1 and %d characters", index, MaxCheckName)
		}
		switch check.Status {
		case CheckPassed, CheckFailed, CheckNotRun:
		default:
			return fmt.Errorf("check %d has invalid status %q", index, check.Status)
		}
		if runeLength(check.Evidence) > MaxEvidence {
			return fmt.Errorf("check %d evidence exceeds %d characters", index, MaxEvidence)
		}
	}
	if len(e.RemainingWork) > MaxRemainingWork {
		return fmt.Errorf("remaining_work exceeds %d items", MaxRemainingWork)
	}
	for index, item := range e.RemainingWork {
		if runeLength(item) < 1 || runeLength(item) > MaxRemainingItem {
			return fmt.Errorf("remaining_work item %d must contain between 1 and %d characters", index, MaxRemainingItem)
		}
	}
	return nil
}

func runeLength(value string) int {
	return utf8.RuneCountInString(value)
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("completion envelope contains duplicate field %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	if err := walk(); err != nil {
		return fmt.Errorf("inspect completion envelope keys: %w", err)
	}
	return nil
}
