package httpapi

import (
	"errors"
	"net/http"

	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/internal/aiserver/store"
)

// recommenderListResponse is the payload of GET /recommendations/recommenders.
type recommenderListResponse struct {
	Recommenders []recommend.Definition `json:"recommenders"`
	// Selected is the user's saved choice for this context, if any, so the UI
	// can restore it without a second request.
	Selected *string        `json:"selected"`
	Config   map[string]any `json:"config"`
}

func (s *Server) handleRecommenders(w http.ResponseWriter, r *http.Request) {
	ctx := recommend.Context(r.URL.Query().Get("context"))
	if !ctx.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "unknown recommendation context")
		return
	}

	resp := recommenderListResponse{
		Recommenders: s.backend.Recommenders().ListForContext(ctx),
		Config:       map[string]any{},
	}

	if db := s.backend.DB(); db != nil {
		if pref, err := db.GetRecommendationPreference(r.Context(), string(ctx)); err == nil {
			id := pref.RecommenderID
			resp.Selected = &id
			resp.Config = pref.Config
		} else if !errors.Is(err, store.ErrNoPreference) {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// queryRequest is the body of POST /recommendations/query.
type queryRequest struct {
	Context       recommend.Context `json:"context"`
	RecommenderID string            `json:"recommenderId"`
	Config        map[string]any    `json:"config"`
	SeedSceneIDs  []int             `json:"seedSceneIds"`
	Limit         *int              `json:"limit"`
	Offset        *int              `json:"offset"`
}

type queryResponse struct {
	Scenes   []recommend.SceneModel `json:"scenes"`
	Meta     recommend.Pagination   `json:"meta"`
	Warnings []string               `json:"warnings"`
}

func (s *Server) handleRecommendationQuery(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	reg, ok := s.backend.Recommenders().Get(req.RecommenderID)
	if !ok {
		writeError(w, http.StatusNotFound, "Recommender not found")
		return
	}

	// Configuration problems are warnings, not failures: the user is tuning a
	// feed, and returning results with a note beats returning an error.
	config, warnings := recommend.ValidateConfig(reg.Definition, req.Config)
	if warnings == nil {
		warnings = []string{}
	}

	offset := 0
	if req.Offset != nil && *req.Offset > 0 {
		offset = *req.Offset
	}

	result, err := reg.Handler(r.Context(), recommend.Request{
		Context:       req.Context,
		RecommenderID: req.RecommenderID,
		Config:        config,
		SeedSceneIDs:  req.SeedSceneIDs,
		Limit:         req.Limit,
		Offset:        offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "recommender_execution_failed: "+err.Error())
		return
	}

	scenes, meta := recommend.ApplyPagination(result, offset, req.Limit)
	writeJSON(w, http.StatusOK, queryResponse{Scenes: scenes, Meta: meta, Warnings: warnings})
}

// preferenceRequest is the body of PUT /recommendations/preferences.
type preferenceRequest struct {
	Context       recommend.Context `json:"context"`
	RecommenderID string            `json:"recommenderId"`
	Config        map[string]any    `json:"config"`
}

type preferenceResponse struct {
	Context       recommend.Context `json:"context"`
	RecommenderID string            `json:"recommenderId"`
	Config        map[string]any    `json:"config"`
	Warnings      []string          `json:"warnings"`
}

func (s *Server) handleRecommendationPreference(w http.ResponseWriter, r *http.Request) {
	var req preferenceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Context.Valid() {
		writeError(w, http.StatusUnprocessableEntity, "unknown recommendation context")
		return
	}

	reg, ok := s.backend.Recommenders().Get(req.RecommenderID)
	if !ok {
		writeError(w, http.StatusNotFound, "Recommender not found")
		return
	}

	config, warnings := recommend.ValidateConfig(reg.Definition, req.Config)
	if warnings == nil {
		warnings = []string{}
	}

	// Only persistable fields are stored, so a transient search box does not
	// come back on the next visit.
	persistable := recommend.PersistableConfig(reg.Definition, config)

	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	saved, err := db.SaveRecommendationPreference(r.Context(), string(req.Context), req.RecommenderID, persistable)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, preferenceResponse{
		Context:       req.Context,
		RecommenderID: saved.RecommenderID,
		Config:        saved.Config,
		Warnings:      warnings,
	})
}
