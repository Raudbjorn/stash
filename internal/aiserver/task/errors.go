package task

import (
	"errors"
	"fmt"
)

// ErrActionNotFound is returned when an action id does not resolve for the
// given context.
var ErrActionNotFound = errors.New("task: action not found")

// ErrActionNotApplicable is returned when an action does not apply to the
// given context.
var ErrActionNotApplicable = errors.New("task: action not applicable to context")

// ErrDuplicateSubmission is returned when a deduplicated action is already
// in-flight for the same context/params. Dup carries the existing record so a
// caller can report it (e.g. "already running") rather than treat it as a
// hard failure.
type ErrDuplicateSubmission struct {
	Dup Record
}

func (e *ErrDuplicateSubmission) Error() string {
	return fmt.Sprintf("task: action already in progress (task %s)", e.Dup.ID)
}
