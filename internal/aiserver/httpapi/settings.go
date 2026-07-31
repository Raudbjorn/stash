package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/internal/aiserver/store"
)

// Settings are served under /plugins even though the server itself owns the
// system ones. That is where the shipped settings UI looks for them, and it
// renders plugin and system settings through the same component.

// settingModel is one setting as the UI expects it.
type settingModel struct {
	Key         string  `json:"key"`
	Type        string  `json:"type"`
	Label       *string `json:"label"`
	Description *string `json:"description"`
	Default     any     `json:"default_value"`
	Options     any     `json:"options"`
	Value       any     `json:"value"`
	// Effective saves the UI from re-implementing the value-or-default rule.
	Effective any `json:"effective_value"`
}

func toSettingModel(s store.Setting) settingModel {
	m := settingModel{
		Key:         s.Key,
		Type:        s.Type,
		Label:       s.Label,
		Description: s.Description,
		Effective:   s.Effective(),
	}
	if s.Default.Valid {
		m.Default = s.Default.Data
	}
	if s.Options.Valid {
		m.Options = s.Options.Data
	}
	if s.Value.Valid {
		m.Value = s.Value.Data
	}
	return m
}

func toSettingModels(in []store.Setting) []settingModel {
	// Never nil: the UI maps over the response.
	out := make([]settingModel, 0, len(in))
	for _, s := range in {
		out = append(out, toSettingModel(s))
	}
	return out
}

// settingUpsert is the body of a settings PUT. A null value resets to default.
type settingUpsert struct {
	Value any `json:"value"`
}

func (s *Server) handleSystemSettingsList(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeJSON(w, http.StatusOK, []settingModel{})
		return
	}

	settings, err := db.ListSettings(r.Context(), store.SystemPluginName)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toSettingModels(settings))
}

func (s *Server) handleSystemSettingUpdate(w http.ResponseWriter, r *http.Request) {
	var body settingUpsert
	if !decodeJSON(w, r, &body) {
		return
	}

	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	updated, err := db.SetSystemSetting(r.Context(), chi.URLParam(r, "key"), body.Value)
	if err != nil {
		writeSettingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettingModel(updated))
}

func (s *Server) handlePluginSettingsList(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeJSON(w, http.StatusOK, []settingModel{})
		return
	}

	settings, err := db.ListSettings(r.Context(), chi.URLParam(r, "plugin"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toSettingModels(settings))
}

func (s *Server) handlePluginSettingUpdate(w http.ResponseWriter, r *http.Request) {
	var body settingUpsert
	if !decodeJSON(w, r, &body) {
		return
	}

	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}

	// Unlike system settings, a plugin setting is created on demand: a plugin
	// may set a value before it has registered a definition for it.
	updated, err := db.SetPluginSetting(r.Context(),
		chi.URLParam(r, "plugin"), chi.URLParam(r, "key"), body.Value)
	if err != nil {
		writeSettingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettingModel(updated))
}

// writeSettingError maps a validation failure to the status and code the
// frontend expects. The codes are matched verbatim in the settings UI.
func writeSettingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrSettingUnknown):
		writeError(w, http.StatusNotFound, "NOT_FOUND")
	case errors.Is(err, store.ErrInvalidNumber):
		writeError(w, http.StatusBadRequest, "INVALID_NUMBER")
	case errors.Is(err, store.ErrInvalidBoolean):
		writeError(w, http.StatusBadRequest, "INVALID_BOOLEAN")
	case errors.Is(err, store.ErrInvalidOption):
		writeError(w, http.StatusBadRequest, "INVALID_OPTION")
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
