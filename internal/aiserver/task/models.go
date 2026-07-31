// Package task is the AI server's scheduler.
//
// It is separate from pkg/job because Stash's job manager runs jobs strictly one
// at a time - load-bearing for Scan/Generate/Clean - whereas AI work needs
// priority queues, per-service concurrency, parent/child groups, submission
// deduplication and cascading cancellation.
package task

import (
	"github.com/stashapp/stash/internal/aiserver/action"
)

// Priority orders the queue. The numeric values are the Python IntEnum's and
// are compared directly, so lower is more urgent.
type Priority int

const (
	PriorityHigh   Priority = 0
	PriorityNormal Priority = 10
	PriorityLow    Priority = 20
)

// String renders the priority as the frontend expects it: the enum's NAME, not
// its number (task summaries carry "high", not 0).
func (p Priority) String() string {
	switch p {
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	case PriorityLow:
		return "low"
	default:
		return "normal"
	}
}

// ParsePriority maps a wire value to a Priority, reporting whether it was one of
// the three accepted names.
func ParsePriority(s string) (Priority, bool) {
	switch s {
	case "high":
		return PriorityHigh, true
	case "normal":
		return PriorityNormal, true
	case "low":
		return PriorityLow, true
	default:
		return PriorityNormal, false
	}
}

// Status is a task's lifecycle state.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
	// StatusStreaming is set by plugins that stream partial results. The core
	// scheduler never sets it, but it must round-trip and it counts as active
	// for deduplication.
	StatusStreaming Status = "streaming"
)

// Terminal reports whether the status is final.
func (s Status) Terminal() bool {
	switch s {
	case StatusCompleted, StatusFailed, StatusCancelled:
		return true
	default:
		return false
	}
}

// Active reports whether a task in this state blocks a duplicate submission.
func (s Status) Active() bool {
	switch s {
	case StatusQueued, StatusRunning, StatusStreaming:
		return true
	default:
		return false
	}
}

// Spec identifies what a task runs.
type Spec struct {
	ID      string
	Service string
}

// Record is the scheduler's state for one task.
//
// Records are never handed out by pointer past the manager boundary: callers
// receive copies, and handlers receive a narrow Handle. The Python original was
// race-free only because asyncio is single-threaded; goroutines are not.
type Record struct {
	ID          string
	ActionID    string
	Service     string
	Priority    Priority
	Status      Status
	SubmittedAt float64 // epoch seconds, as the frontend reads them
	StartedAt   *float64
	FinishedAt  *float64

	Context action.ContextInput
	Params  map[string]any

	Result any
	Error  string

	// CancelRequested is distinct from a cancelled status: a running task is
	// flagged first and only becomes cancelled once it observes the request.
	// The REST response exposes both.
	CancelRequested bool

	// GroupID links a child to its parent (a fan-out batch). Only tasks with no
	// group are persisted to history.
	GroupID string

	// SkipConcurrency marks a controller task that has released its slot.
	SkipConcurrency bool

	dedupeContextKey string
	dedupeParamsKey  string
}

// Summary is the payload broadcast over the task websocket and used wherever a
// task is described to the frontend.
//
// The keys are snake_case and the exact set the TypeScript reads. Note `result`
// is only populated for completed tasks - a deliberate quirk of the original
// that avoids shipping partial results.
func (r Record) Summary() map[string]any {
	var result any
	if r.Status == StatusCompleted {
		result = r.Result
	}

	var errValue any
	if r.Error != "" {
		errValue = r.Error
	}

	var groupID any
	if r.GroupID != "" {
		groupID = r.GroupID
	}

	return map[string]any{
		"id":               r.ID,
		"action_id":        r.ActionID,
		"service":          r.Service,
		"priority":         r.Priority.String(),
		"status":           string(r.Status),
		"submitted_at":     r.SubmittedAt,
		"started_at":       r.StartedAt,
		"finished_at":      r.FinishedAt,
		"error":            errValue,
		"cancel_requested": r.CancelRequested,
		"result":           result,
		"group_id":         groupID,
	}
}
