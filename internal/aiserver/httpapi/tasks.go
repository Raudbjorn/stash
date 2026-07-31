package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// taskResponse is the REST view of a task.
//
// Deliberately NOT the same shape as the websocket summary: this omits
// submitted_at and cancel_requested. Both shapes are what the frontend expects,
// so neither can be "unified" without changing it.
type taskResponse struct {
	ID         string   `json:"id"`
	ActionID   string   `json:"action_id"`
	Service    string   `json:"service"`
	Priority   string   `json:"priority"`
	Status     string   `json:"status"`
	Error      *string  `json:"error"`
	Result     any      `json:"result"`
	GroupID    *string  `json:"group_id"`
	StartedAt  *float64 `json:"started_at"`
	FinishedAt *float64 `json:"finished_at"`
}

func toTaskResponse(rec task.Record) taskResponse {
	resp := taskResponse{
		ID:         rec.ID,
		ActionID:   rec.ActionID,
		Service:    rec.Service,
		Priority:   rec.Priority.String(),
		Status:     string(rec.Status),
		Result:     rec.Result,
		StartedAt:  rec.StartedAt,
		FinishedAt: rec.FinishedAt,
	}
	if rec.Error != "" {
		e := rec.Error
		resp.Error = &e
	}
	if rec.GroupID != "" {
		g := rec.GroupID
		resp.GroupID = &g
	}
	return resp
}

type submitTaskRequest struct {
	ActionID string              `json:"action_id"`
	Context  action.ContextInput `json:"context"`
	Params   map[string]any      `json:"params"`
	Priority *string             `json:"priority"`
}

type submitTaskResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

// handleTaskSubmit resolves an action and queues it.
//
// Note the default priority differs from /actions/submit: this endpoint
// defaults to normal rather than inferring from the view. The divergence is
// intentional and preserved.
func (s *Server) handleTaskSubmit(w http.ResponseWriter, r *http.Request) {
	var req submitTaskRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	reg, ok := s.backend.Actions().Resolve(req.ActionID, req.Context)
	if !ok {
		writeError(w, http.StatusNotFound, "Action not found")
		return
	}

	priority := task.PriorityNormal
	if req.Priority != nil {
		if p, valid := task.ParsePriority(*req.Priority); valid {
			priority = p
		}
	}

	rec, err := s.backend.Tasks().Submit(reg.Definition, reg.Handler, req.Context, req.Params, priority, task.SubmitOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, submitTaskResponse{TaskID: rec.ID, Status: string(rec.Status)})
}

func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	rec, err := s.backend.Tasks().Get(chi.URLParam(r, "task_id"))
	if errors.Is(err, task.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Task not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(rec))
}

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	if !s.backend.Tasks().Cancel(chi.URLParam(r, "task_id")) {
		// Unknown and already-terminal are reported the same way, matching the
		// original: there is nothing to cancel in either case.
		writeError(w, http.StatusNotFound, "Task not found or cannot cancel")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

type listTasksResponse struct {
	Tasks []taskResponse `json:"tasks"`
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	filter := task.ListFilter{Service: r.URL.Query().Get("service")}

	// An unrecognised status is ignored rather than rejected, matching the
	// original's try/except around the enum lookup.
	if raw := r.URL.Query().Get("status"); raw != "" {
		switch task.Status(raw) {
		case task.StatusQueued, task.StatusRunning, task.StatusCompleted,
			task.StatusFailed, task.StatusCancelled, task.StatusStreaming:
			filter.Status = task.Status(raw)
		}
	}

	records := s.backend.Tasks().List(filter)
	out := make([]taskResponse, 0, len(records))
	for _, rec := range records {
		out = append(out, toTaskResponse(rec))
	}
	writeJSON(w, http.StatusOK, listTasksResponse{Tasks: out})
}

func (s *Server) handleTaskHistory(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"history": []any{}})
		return
	}

	filter := store.TaskHistoryFilter{
		Service: r.URL.Query().Get("service"),
		Status:  r.URL.Query().Get("status"),
		Limit:   50,
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			filter.Limit = n
		}
	}

	rows, err := db.ListTaskHistory(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": rows})
}
