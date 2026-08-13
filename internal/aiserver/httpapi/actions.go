package httpapi

import (
	"errors"
	"net/http"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// availableRequest is the body of POST /actions/available.
//
// The context is wrapped in an object rather than sent bare: FastAPI's
// Body(..., embed=True) produced {"context": {...}} and the frontend still
// sends that shape.
type availableRequest struct {
	Context action.ContextInput `json:"context"`
}

func (s *Server) handleActionsAvailable(w http.ResponseWriter, r *http.Request) {
	var req availableRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// A bare JSON array, not an object - the frontend maps over the response.
	writeJSON(w, http.StatusOK, s.backend.Actions().Available(req.Context))
}

// submitActionRequest is the body of POST /actions/submit.
type submitActionRequest struct {
	ActionID string              `json:"action_id"`
	Context  action.ContextInput `json:"context"`
	Params   map[string]any      `json:"params"`
	// Priority optionally overrides the inferred value.
	Priority *string `json:"priority"`
}

type submitActionResponse struct {
	TaskID           string `json:"task_id"`
	Status           string `json:"status"`
	InferredPriority string `json:"inferred_priority"`
}

func (s *Server) handleActionSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitActionRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	rec, err := s.backend.SubmitAction(r.Context(), req.ActionID, req.Context, req.Params, req.Priority)
	if err != nil {
		var dup *task.ErrDuplicateSubmission
		switch {
		case errors.As(err, &dup):
			// A structured detail object, parsed field-by-field by the
			// frontend to show "already running" instead of an error.
			writeError(w, http.StatusConflict, map[string]any{
				"code":    "ACTION_ALREADY_IN_PROGRESS",
				"task_id": dup.Dup.ID,
				"status":  string(dup.Dup.Status),
				"message": "Action is already processing for this selection.",
			})
		case errors.Is(err, task.ErrActionNotFound):
			writeError(w, http.StatusNotFound, "Action not found")
		case errors.Is(err, task.ErrActionNotApplicable):
			writeError(w, http.StatusBadRequest, "Action not applicable to provided context")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}

	inferred := "low"
	if req.Context.IsDetailView {
		inferred = "high"
	}
	if req.Priority != nil {
		if _, valid := task.ParsePriority(*req.Priority); valid {
			inferred = *req.Priority
		}
	}

	writeJSON(w, http.StatusOK, submitActionResponse{
		TaskID:           rec.ID,
		Status:           string(rec.Status),
		InferredPriority: inferred,
	})
}
