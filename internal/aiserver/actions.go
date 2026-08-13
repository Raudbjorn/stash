package aiserver

import (
	"context"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// SubmitAction resolves and submits an action.
//
// Shared by the REST /actions/submit handler and GraphQL mutations so both
// apply the same resolve -> applicability -> duplicate-check -> priority
// inference sequence instead of it living twice. Errors are the sentinels in
// package task (ErrActionNotFound, ErrActionNotApplicable,
// ErrDuplicateSubmission) so callers outside this package - including
// internal/aiserver/httpapi, which must not import this package - can branch
// on them without a dependency cycle.
func (s *Server) SubmitAction(ctx context.Context, actionID string, actx action.ContextInput, params map[string]any, priority *string) (task.Record, error) {
	if !s.Ready() {
		return task.Record{}, ErrDisabled
	}

	reg, ok := s.Actions().Resolve(actionID, actx)
	if !ok {
		return task.Record{}, task.ErrActionNotFound
	}
	if !reg.Definition.IsApplicable(actx) {
		return task.Record{}, task.ErrActionNotApplicable
	}

	tasks := s.Tasks()
	if tasks == nil {
		return task.Record{}, ErrDisabled
	}

	if reg.Definition.DeduplicateSubmissions {
		if dup, found := tasks.FindDuplicate(reg.Definition, actx, params); found {
			return task.Record{}, &task.ErrDuplicateSubmission{Dup: dup}
		}
	}

	// Detail views are interactive and jump the queue; bulk library actions are
	// background work. An explicit priority overrides the inference.
	inferred := "low"
	if actx.IsDetailView {
		inferred = "high"
	}
	if priority != nil {
		if _, valid := task.ParsePriority(*priority); valid {
			inferred = *priority
		}
	}
	p, _ := task.ParsePriority(inferred)

	return tasks.Submit(reg.Definition, reg.Handler, actx, params, p, task.SubmitOptions{})
}
