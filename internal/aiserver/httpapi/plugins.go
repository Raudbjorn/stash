package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/internal/aiserver/catalog"
	"github.com/stashapp/stash/internal/aiserver/store"
)

// Plugin management.
//
// Most of this answers with the plugin host stopped, which is the point: Go
// owns what is installed, where it came from and what a change would entail, so
// browsing and planning keep working while the Python process is restarting.
// Only reload needs a host, and it says so.

// pluginsBackend is the slice of the server this file needs.
//
// A separate interface from Backend so the plugin endpoints can be wired
// independently of the rest, and so a build without a catalog still compiles.
type pluginsBackend interface {
	// Catalog manages sources, the index and installation. Nil when the
	// subsystem is not running.
	Catalog() *catalog.Manager
	// ReloadPlugins recycles the plugin host. Returns false when there is no
	// host to recycle.
	ReloadPlugins() bool
	// ServePluginRoute proxies a request to a plugin's own HTTP route,
	// reporting whether any plugin claimed it.
	ServePluginRoute(w http.ResponseWriter, r *http.Request, rest string) bool
}

// handlePluginRoute proxies anything under /plugins/ that the literal routes
// above did not claim.
//
// Registered last and matched only as a fallback, so a plugin cannot shadow
// /plugins/installed or the settings endpoints - which the Python server, where
// plugins chose their own FastAPI mount point, could not prevent.
func (s *Server) handlePluginRoute(w http.ResponseWriter, r *http.Request) {
	rest := chi.URLParam(r, "*")

	backend, ok := s.plugins()
	if ok && backend.ServePluginRoute(w, r, rest) {
		return
	}
	writeError(w, http.StatusNotFound, "no plugin serves "+rest)
}

func (s *Server) plugins() (pluginsBackend, bool) {
	backend, ok := s.backend.(pluginsBackend)
	return backend, ok
}

// catalogManager returns the catalog manager, or writes a 503 and returns nil.
func (s *Server) catalogManager(w http.ResponseWriter) *catalog.Manager {
	backend, ok := s.plugins()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return nil
	}
	manager := backend.Catalog()
	if manager == nil {
		writeError(w, http.StatusServiceUnavailable, "plugin management is unavailable")
		return nil
	}
	return manager
}

// GET /plugins/installed
func (s *Server) handlePluginsInstalled(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "the AI database is not available")
		return
	}

	activeOnly := r.URL.Query().Get("active_only") == "true"
	includeRemoved := r.URL.Query().Get("include_removed") == "true"

	metas, err := db.ListPluginMeta(r.Context(), activeOnly, includeRemoved)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, metas)
}

// GET /plugins/sources
func (s *Server) handlePluginSources(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "the AI database is not available")
		return
	}

	sources, err := db.ListPluginSources(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sources)
}

// sourceCreate is the body of POST /plugins/sources.
type sourceCreate struct {
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled *bool  `json:"enabled"`
}

// POST /plugins/sources
func (s *Server) handlePluginSourceCreate(w http.ResponseWriter, r *http.Request) {
	var body sourceCreate
	if !decodeJSON(w, r, &body) {
		return
	}

	body.Name = strings.TrimSpace(body.Name)
	body.URL = strings.TrimSpace(body.URL)
	if body.Name == "" || body.URL == "" {
		writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}

	// Rejected here rather than at refresh time, so the user finds out while
	// they are still looking at the form they typed it into.
	if err := catalog.ValidateSourceURL(body.URL); err != nil {
		writeError(w, http.StatusBadRequest, map[string]any{
			"code":    "INSECURE_SOURCE",
			"message": err.Error(),
		})
		return
	}

	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "the AI database is not available")
		return
	}

	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}

	source, err := db.UpsertPluginSource(r.Context(), body.Name, body.URL, enabled)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, source)
}

// DELETE /plugins/sources/{source_name}
func (s *Server) handlePluginSourceDelete(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "the AI database is not available")
		return
	}

	if err := db.DeletePluginSource(r.Context(), chi.URLParam(r, "source_name")); err != nil {
		writePluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// POST /plugins/sources/{source_name}/refresh
func (s *Server) handlePluginSourceRefresh(w http.ResponseWriter, r *http.Request) {
	manager := s.catalogManager(w)
	if manager == nil {
		return
	}

	result, err := manager.Refresh(r.Context(), chi.URLParam(r, "source_name"))
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /plugins/catalog/{source_name}
func (s *Server) handlePluginCatalog(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "the AI database is not available")
		return
	}

	source, err := db.GetPluginSource(r.Context(), chi.URLParam(r, "source_name"))
	if err != nil {
		writePluginError(w, err)
		return
	}

	entries, err := db.ListCatalog(r.Context(), source.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// pluginRequest is the body shared by the plan and lifecycle endpoints.
type pluginRequest struct {
	Plugin string `json:"plugin"`
	Source string `json:"source"`

	Overwrite           bool `json:"overwrite"`
	InstallDependencies bool `json:"install_dependencies"`

	Cascade  bool `json:"cascade"`
	DropData bool `json:"drop_data"`
}

// POST /plugins/install/plan
func (s *Server) handlePluginInstallPlan(w http.ResponseWriter, r *http.Request) {
	manager := s.catalogManager(w)
	if manager == nil {
		return
	}

	var body pluginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Plugin == "" {
		writeError(w, http.StatusBadRequest, "PLUGIN_REQUIRED")
		return
	}

	plan, err := manager.PlanInstall(r.Context(), body.Plugin, body.Source)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// POST /plugins/install
func (s *Server) handlePluginInstall(w http.ResponseWriter, r *http.Request) {
	s.installOrUpdate(w, r, false)
}

// POST /plugins/update
//
// An update is an install that has already answered both confirmations: the
// user asked for a newer version of something they have, so overwriting it and
// bringing its dependencies along is the request rather than a surprise.
func (s *Server) handlePluginUpdate(w http.ResponseWriter, r *http.Request) {
	s.installOrUpdate(w, r, true)
}

func (s *Server) installOrUpdate(w http.ResponseWriter, r *http.Request, isUpdate bool) {
	manager := s.catalogManager(w)
	if manager == nil {
		return
	}

	var body pluginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Plugin == "" {
		writeError(w, http.StatusBadRequest, "PLUGIN_REQUIRED")
		return
	}

	overwrite, withDeps := body.Overwrite, body.InstallDependencies
	if isUpdate {
		overwrite, withDeps = true, true
	}

	result, err := manager.Install(r.Context(), body.Plugin, body.Source, overwrite, withDeps)
	if err != nil {
		writePluginError(w, err)
		return
	}
	if isUpdate {
		result.Status = "updated"
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /plugins/remove/plan
func (s *Server) handlePluginRemovePlan(w http.ResponseWriter, r *http.Request) {
	manager := s.catalogManager(w)
	if manager == nil {
		return
	}

	var body pluginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Plugin == "" {
		writeError(w, http.StatusBadRequest, "PLUGIN_REQUIRED")
		return
	}

	plan, err := manager.PlanRemove(r.Context(), body.Plugin)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// POST /plugins/remove
func (s *Server) handlePluginRemove(w http.ResponseWriter, r *http.Request) {
	manager := s.catalogManager(w)
	if manager == nil {
		return
	}

	var body pluginRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Plugin == "" {
		writeError(w, http.StatusBadRequest, "PLUGIN_REQUIRED")
		return
	}

	result, err := manager.Remove(r.Context(), body.Plugin, body.Cascade, body.DropData)
	if err != nil {
		writePluginError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// POST /plugins/reload
//
// The only endpoint here that genuinely needs a running host: reloading means
// recycling the process, and there is nothing to recycle when it is down.
func (s *Server) handlePluginReload(w http.ResponseWriter, r *http.Request) {
	var body pluginRequest
	if !decodeJSON(w, r, &body) {
		return
	}

	backend, ok := s.plugins()
	if !ok || !backend.ReloadPlugins() {
		writeError(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "HOST_UNAVAILABLE",
			"message": "The plugin host is not running, so there is nothing to reload.",
		})
		return
	}

	// Reload is asynchronous - the host restarts and re-imports - so this
	// reports that it was accepted, not that it finished.
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "reloading",
		"plugin": body.Plugin,
	})
}

// writePluginError maps a domain error to the status and body the UI expects.
//
// The codes are the Python server's, because the shipped TypeScript switches on
// them to decide which confirmation dialog to show.
func writePluginError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "NOT_FOUND")

	case errors.Is(err, store.ErrSourceImmutable):
		writeError(w, http.StatusBadRequest, "SOURCE_IMMUTABLE")

	case errors.Is(err, catalog.ErrInsecureSource):
		writeError(w, http.StatusBadRequest, map[string]any{
			"code":    "INSECURE_SOURCE",
			"message": err.Error(),
		})

	case errors.Is(err, catalog.ErrAlreadyInstalled):
		writeError(w, http.StatusConflict, map[string]any{
			"code":    "ALREADY_INSTALLED",
			"message": err.Error(),
		})

	case errors.Is(err, catalog.ErrDigestMismatch), errors.Is(err, catalog.ErrUnsafePath):
		// A checksum failure or a traversal attempt is not a routine error; the
		// download was actively wrong and the user should see that plainly.
		writeError(w, http.StatusBadGateway, map[string]any{
			"code":    "UNTRUSTED_DOWNLOAD",
			"message": err.Error(),
		})

	default:
		var needsDeps *catalog.DependenciesRequiredError
		if errors.As(err, &needsDeps) {
			writeError(w, http.StatusConflict, map[string]any{
				"code":         "DEPENDENCIES_REQUIRED",
				"dependencies": needsDeps.Dependencies,
				"message":      err.Error(),
			})
			return
		}

		var dependents *catalog.DependentsError
		if errors.As(err, &dependents) {
			writeError(w, http.StatusConflict, map[string]any{
				"code":       "DEPENDENT_PLUGINS",
				"dependents": dependents.Dependents,
				"message":    err.Error(),
			})
			return
		}

		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
