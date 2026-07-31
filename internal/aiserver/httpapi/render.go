// Package httpapi serves the /api/v1 surface the Stash UI plugin talks to.
//
// The shapes here are a compatibility contract, not a design: the shipped
// TypeScript is unmodified, so status codes, field names and error bodies must
// match what the Python FastAPI server produced. Two conventions in particular
// look inconsistent and are load-bearing:
//   - Request contexts use camelCase (entityId, isDetailView, ...).
//   - Task summaries and responses use snake_case (action_id, submitted_at).
package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/stashapp/stash/pkg/logger"
)

// writeJSON emits a JSON response.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already sent; all that is left is to record it.
		logger.Errorf("writing AI API response: %v", err)
	}
}

// writeError emits FastAPI's error shape, {"detail": ...}, which the frontend
// parses directly. Detail is a string for simple errors and an object for
// structured ones such as the 409 duplicate-submission response.
func writeError(w http.ResponseWriter, status int, detail any) {
	writeJSON(w, status, map[string]any{"detail": detail})
}

// decodeJSON reads a JSON request body, replying with 422 on malformed input to
// match FastAPI's validation status.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid request body: "+err.Error())
		return false
	}
	return true
}

// encodeJSON marshals a payload for the websocket, where messages are framed
// rather than streamed to a ResponseWriter.
func encodeJSON(payload any) ([]byte, error) {
	return json.Marshal(payload)
}
