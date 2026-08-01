package hostauthority

import (
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type BindingKind string

const (
	BindingUnproven BindingKind = "unproven"
	BindingSameHost BindingKind = "same_host"
)

// AuthorityBinding describes how the App Server connection was attached. A
// separately spawned proxy is deliberately unproven even if it can read the
// same on-disk thread. Only a host-provided attachment with a stable instance
// identity can claim same-host authority.
type AuthorityBinding struct {
	Kind           BindingKind
	HostInstanceID string
}

func (b AuthorityBinding) SameHostVerified() bool {
	return b.Kind == BindingSameHost && b.HostInstanceID != ""
}

type ThreadActiveFlag string

const (
	FlagWaitingOnApproval ThreadActiveFlag = "waitingOnApproval"
	FlagWaitingOnUser     ThreadActiveFlag = "waitingOnUserInput"
)

type ThreadStatus struct {
	Type        string             `json:"type"`
	ActiveFlags []ThreadActiveFlag `json:"activeFlags,omitempty"`
}

func (s ThreadStatus) IsActive() bool { return s.Type == "active" }
func (s ThreadStatus) IsIdle() bool   { return s.Type == "idle" }
func (s ThreadStatus) IsKnown() bool  { return s.IsActive() || s.IsIdle() }

func (s ThreadStatus) Waiting() bool {
	for _, flag := range s.ActiveFlags {
		if flag == FlagWaitingOnApproval || flag == FlagWaitingOnUser {
			return true
		}
	}
	return false
}

type Turn struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type Thread struct {
	ID     string       `json:"id"`
	CWD    string       `json:"cwd,omitempty"`
	Status ThreadStatus `json:"status"`
	Turns  []Turn       `json:"turns,omitempty"`
}

func (t Thread) LastTurn() (Turn, bool) {
	if len(t.Turns) == 0 {
		return Turn{}, false
	}
	return t.Turns[len(t.Turns)-1], true
}

type HookError struct {
	Message string `json:"message"`
	Path    string `json:"path"`
}

type Hook struct {
	Command       *string `json:"command"`
	CurrentHash   string  `json:"currentHash"`
	DisplayOrder  int64   `json:"displayOrder"`
	Enabled       bool    `json:"enabled"`
	EventName     string  `json:"eventName"`
	HandlerType   string  `json:"handlerType"`
	IsManaged     bool    `json:"isManaged"`
	Key           string  `json:"key"`
	Matcher       *string `json:"matcher"`
	PluginID      *string `json:"pluginId"`
	Source        string  `json:"source"`
	SourcePath    string  `json:"sourcePath"`
	TimeoutSec    uint64  `json:"timeoutSec"`
	TrustStatus   string  `json:"trustStatus"`
	StatusMessage *string `json:"statusMessage"`
}

type HookInventory struct {
	CWD      string      `json:"cwd"`
	Errors   []HookError `json:"errors"`
	Hooks    []Hook      `json:"hooks"`
	Warnings []string    `json:"warnings"`
}

type HookDecision struct {
	Compatible  bool     `json:"compatible"`
	Fingerprint string   `json:"fingerprint"`
	Reasons     []string `json:"reasons,omitempty"`
}

type Snapshot struct {
	CapturedAt              time.Time     `json:"captured_at"`
	Target                  Thread        `json:"target"`
	LoadedThreads           []Thread      `json:"loaded_threads"`
	DescendantThreads       []Thread      `json:"descendant_threads"`
	BackgroundTerminalCount int           `json:"background_terminal_count"`
	Hooks                   HookInventory `json:"hooks"`
	HookDecision            HookDecision  `json:"hook_decision"`
	SameHostProven          bool          `json:"same_host_proven"`
	InventoryComplete       bool          `json:"inventory_complete"`
	EventLossDetected       bool          `json:"event_loss_detected"`
	LiveTargetObserved      bool          `json:"live_target_observed"`
	CompletedTurnIDs        []string      `json:"completed_turn_ids,omitempty"`
	IncompleteHookCount     int           `json:"incomplete_hook_count"`
	HookFailureDetected     bool          `json:"hook_failure_detected"`
	HostInstanceID          string        `json:"host_instance_id,omitempty"`
	Reasons                 []string      `json:"reasons,omitempty"`
}

func (s Snapshot) ReadyForFinalGate(expectedTurnID string) bool {
	if !s.SameHostProven || !s.LiveTargetObserved || !s.InventoryComplete || s.EventLossDetected ||
		!s.HookDecision.Compatible || s.IncompleteHookCount != 0 || s.HookFailureDetected {
		return false
	}
	if !s.Target.Status.IsIdle() || s.Target.Status.Waiting() || s.BackgroundTerminalCount != 0 {
		return false
	}
	last, ok := s.Target.LastTurn()
	if !ok || last.ID != expectedTurnID || last.Status != "completed" {
		return false
	}
	turnEventObserved := false
	for _, turnID := range s.CompletedTurnIDs {
		if turnID == expectedTurnID {
			turnEventObserved = true
			break
		}
	}
	if !turnEventObserved {
		return false
	}
	for _, thread := range s.LoadedThreads {
		if thread.ID != s.Target.ID && !thread.Status.IsIdle() {
			return false
		}
	}
	for _, thread := range s.DescendantThreads {
		if !thread.Status.IsIdle() {
			return false
		}
	}
	return true
}

func WorkspacePathMatches(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" || !filepath.IsAbs(left) || !filepath.IsAbs(right) {
		return false
	}
	leftAbsolute, leftErr := filepath.Abs(left)
	rightAbsolute, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftAbsolute = filepath.Clean(leftAbsolute)
	rightAbsolute = filepath.Clean(rightAbsolute)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(leftAbsolute, rightAbsolute)
	}
	return leftAbsolute == rightAbsolute
}
