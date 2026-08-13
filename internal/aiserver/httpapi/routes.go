package httpapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/interactions"
	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// Backend is what the HTTP layer needs from the AI server. An interface rather
// than the concrete type so this package does not import its own parent.
type Backend interface {
	// Ready reports whether the subsystem is up. When false every route
	// answers 503 rather than panicking on a nil database.
	Ready() bool
	// Actions is the action registry.
	Actions() *action.Registry
	// Tasks is the scheduler.
	Tasks() *task.Manager
	// SubmitAction resolves and submits an action, applying the shared
	// resolve/applicability/duplicate-check/priority-inference sequence.
	SubmitAction(ctx context.Context, actionID string, actx action.ContextInput, params map[string]any, priority *string) (task.Record, error)
	// DB is the AI database. May be nil when not ready.
	DB() *store.DB
	// Interactions ingests watch-tracking events. May be nil when not ready.
	Interactions() *interactions.Service
	// Recommenders is the recommender registry.
	Recommenders() *recommend.Registry
	// HealthSnapshot summarises the subsystem for the health endpoint.
	// Returns any so this package need not import the server's own types.
	HealthSnapshot(ctx context.Context) any
	// Version reports the backend version advertised to plugins and the UI.
	Version() VersionInfo
}

// VersionInfo is the payload of GET /api/v1/version.
type VersionInfo struct {
	// BackendVersion must satisfy the `required_backend` constraints declared
	// by existing plugins (for example ">=0.9.3"), or they refuse to load.
	BackendVersion string `json:"backend_version"`
	// FrontendMinVersion is echoed for the frontend to compare against itself;
	// the comparison happens client-side.
	FrontendMinVersion *string `json:"frontend_min_version"`
	// SchemaVersion replaces the Python server's alembic head. The key name is
	// kept because the TypeScript reads it.
	SchemaVersion string `json:"db_alembic_head"`
}

// Server owns the /api/v1 router.
type Server struct {
	backend Backend
	hub     *wsHub
}

// New builds the API server and starts its websocket hub.
func New(backend Backend) *Server {
	s := &Server{backend: backend, hub: newWSHub(backend)}
	s.hub.start()
	return s
}

// Close stops the websocket hub.
func (s *Server) Close() {
	if s != nil && s.hub != nil {
		s.hub.stop()
	}
}

// Routes returns the router mounted at /api/v1.
func (s *Server) Routes() chi.Router {
	r := chi.NewRouter()

	// Always reachable, even when the subsystem is off.
	//
	// The frontend polls /version to decide whether a backend exists at all,
	// and renders /system/health to explain what is wrong - answering 503 on
	// either leaves the settings page unable to say anything useful. Both are
	// registered outside the readiness group rather than exempted by path,
	// because chi does not rewrite r.URL.Path inside a mounted router and a
	// path comparison there silently never matches.
	r.Get("/version", s.handleVersion)
	r.Get("/system/health", s.handleHealth)
	// Evaluation reports a typed provider error even while AI is disabled, so
	// it sits outside the generic readiness middleware.
	r.Post("/tagging/vlm/eval", s.handleVLMEval)

	// Everything else requires the subsystem to be running. The check lives
	// here so no individual handler has to repeat it.
	r.Group(func(r chi.Router) {
		r.Use(s.requireReady)

		r.Route("/actions", func(r chi.Router) {
			r.Post("/available", s.handleActionsAvailable)
			r.Post("/submit", s.handleActionSubmit)
		})

		r.Route("/tasks", func(r chi.Router) {
			// Registered before /{task_id} so the literal path wins. chi would
			// resolve this correctly either way, but the ordering is explicit
			// because the Python server depended on it.
			r.Get("/history", s.handleTaskHistory)
			r.Post("/submit", s.handleTaskSubmit)
			r.Get("/", s.handleTaskList)
			r.Get("/{task_id}", s.handleTaskGet)
			r.Post("/{task_id}/cancel", s.handleTaskCancel)
		})

		r.Post("/interactions/sync", s.handleInteractionsSync)

		// AI tagging. Analysis itself is submitted as an action so it runs on
		// the scheduler; these endpoints are the fast operations around it.
		r.Route("/tagging", func(r chi.Router) {
			r.Get("/status", s.handleTaggingStatus)
			r.Get("/heads", s.handleTaggingHeads)
			r.Get("/spans/{scene_id}", s.handleTaggingSpans)
			r.Post("/regenerate", s.handleTaggingRegenerate)
			r.Post("/train", s.handleTaggingTrain)
		})

		r.Route("/recommendations", func(r chi.Router) {
			r.Get("/recommenders", s.handleRecommenders)
			r.Post("/query", s.handleRecommendationQuery)
			r.Put("/preferences", s.handleRecommendationPreference)
		})

		// Settings live under /plugins because that is where the shipped
		// settings UI looks, even for the server's own system settings.
		r.Route("/plugins", func(r chi.Router) {
			r.Get("/system/settings", s.handleSystemSettingsList)
			r.Put("/system/settings/{key}", s.handleSystemSettingUpdate)
			r.Get("/settings/{plugin}", s.handlePluginSettingsList)
			r.Put("/settings/{plugin}/{key}", s.handlePluginSettingUpdate)

			// Plugin management. All of this answers with the Python host
			// stopped except /reload, because Go owns what is installed, what
			// a catalog advertises and what a change would entail.
			r.Get("/installed", s.handlePluginsInstalled)

			r.Get("/sources", s.handlePluginSources)
			r.Post("/sources", s.handlePluginSourceCreate)
			r.Delete("/sources/{source_name}", s.handlePluginSourceDelete)
			r.Post("/sources/{source_name}/refresh", s.handlePluginSourceRefresh)

			r.Get("/catalog/{source_name}", s.handlePluginCatalog)

			r.Post("/install/plan", s.handlePluginInstallPlan)
			r.Post("/install", s.handlePluginInstall)
			r.Post("/update", s.handlePluginUpdate)
			r.Post("/remove/plan", s.handlePluginRemovePlan)
			r.Post("/remove", s.handlePluginRemove)
			r.Post("/reload", s.handlePluginReload)

			// Plugin-served routes, matched only when nothing above claimed
			// the path. chi prefers literal segments over a wildcard, so the
			// server's own endpoints always win.
			r.HandleFunc("/*", s.handlePluginRoute)
		})

		r.Get("/ws/tasks", s.hub.handleWS)
	})

	return r
}

// requireReady short-circuits with 503 when the subsystem is disabled or failed.
func (s *Server) requireReady(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.backend.Ready() {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "AI_UNAVAILABLE",
			"message": "The AI server is not enabled or failed to start.",
		})
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend.HealthSnapshot(r.Context()))
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend.Version())
}
