// Package aiserver hosts the AI feature set that previously ran as a separate
// Python FastAPI process alongside Stash.
//
// It owns its own database file, its own task scheduler, and the /api/v1
// surface the existing Stash UI plugin talks to. Everything is opt-in: with
// ai_enabled false, New returns a server that starts nothing, creates no files,
// and answers every route with 503.
//
// Dependency direction is deliberate and one-way: internal/manager constructs
// this package, so this package must never import internal/manager. Access to
// Stash's data arrives through the injected models.Repository.
package aiserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/catalog"
	"github.com/stashapp/stash/internal/aiserver/host"
	"github.com/stashapp/stash/internal/aiserver/hostconn"
	"github.com/stashapp/stash/internal/aiserver/httpapi"
	"github.com/stashapp/stash/internal/aiserver/interactions"
	"github.com/stashapp/stash/internal/aiserver/pluginhost"
	"github.com/stashapp/stash/internal/aiserver/pyhost"
	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/tagging"
	"github.com/stashapp/stash/internal/aiserver/task"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
)

// BackendVersion is what this server reports to plugins and the UI.
//
// Existing plugins declare constraints such as `required_backend: ">=0.9.3"`
// and refuse to load otherwise, so this must stay at or above the version of
// the Python server being replaced.
const BackendVersion = "0.9.3"

// FrontendMinVersion is echoed to the UI, which compares it against itself.
const FrontendMinVersion = ">=0.8.0"

// Deps are everything the AI server needs from the rest of Stash.
type Deps struct {
	// Repo gives native access to Stash's own data. This replaces the Python
	// server's two integration paths at once: its GraphQL client and its
	// read-only SQLite connection to Stash's database file.
	Repo models.Repository

	// Config is Stash's configuration. Read lazily rather than snapshotted, so
	// that toggling ai_enabled takes effect on the next Refresh.
	Config *config.Config
}

// State describes the server's lifecycle for diagnostics and the health
// endpoint.
type State string

const (
	// StateDisabled means ai_enabled is false. This is the default.
	StateDisabled State = "disabled"
	// StateStopped means enabled but not yet started, or shut down.
	StateStopped State = "stopped"
	// StateReady means the database is open and migrated.
	StateReady State = "ready"
	// StateFailed means startup failed; Err carries the reason. Stash itself
	// keeps running - an AI failure must never take the server down.
	StateFailed State = "failed"
)

// ErrDisabled is returned by operations attempted while the server is off.
var ErrDisabled = errors.New("aiserver: disabled")

// Server is the composition root for the AI feature set.
//
// It is safe for concurrent use. Start and Shutdown may be called repeatedly;
// both are idempotent.
type Server struct {
	deps Deps

	// These outlive individual Start/Shutdown cycles so registrations and
	// websocket subscribers survive an enable/disable toggle.
	actions      *action.Registry
	services     *service.Registry
	recommenders *recommend.Registry
	api          *httpapi.Server

	// pluginHost supervises the Python plugin process. It outlives individual
	// Start/Shutdown cycles so its crash history survives a toggle.
	pluginHost *host.Supervisor
	plugins    *pluginhost.Manager
	catalog    *catalog.Manager

	tagging        *tagging.Service
	trainer        *tagging.Trainer
	taggingStatus  tagging.Status
	voyageSegments *recommend.VoyageSegmentIndex

	// graphQL is Stash's own API handler, registered after construction.
	graphQL http.Handler

	mu           sync.RWMutex
	state        State
	err          error
	db           *store.DB
	tasks        *task.Manager
	interactions *interactions.Service

	// refreshMu serializes Refresh so two concurrent configuration changes
	// cannot interleave a stop/start cycle. Shutdown and Start each acquire mu
	// separately rather than holding it for the whole call, so without this,
	// two overlapping Refresh calls could run as
	// Shutdown/Shutdown/Start/Start - the second Start would then find the
	// first one's state already StateReady and return early, leaving the
	// subsystem built from whichever config lost the race, registrations
	// possibly withdrawn by the wrong Shutdown, and no error surfaced.
	refreshMu sync.Mutex
}

// New builds a server. It performs no I/O: the database is opened by Start,
// which runs after Stash's own database is available and the config path is
// known to be valid.
func New(deps Deps) *Server {
	s := &Server{
		deps:         deps,
		state:        StateStopped,
		actions:      action.NewRegistry(),
		services:     service.NewRegistry(),
		recommenders: recommend.NewRegistry(),
	}
	s.api = httpapi.New(s)
	return s
}

// Routes returns the router mounted at /api/v1. Always non-nil: when the
// subsystem is disabled every route answers 503, so the mount in Stash's server
// is unconditional and the enable check lives in one place.
func (s *Server) Routes() chi.Router { return s.api.Routes() }

// Actions exposes the action registry.
func (s *Server) Actions() *action.Registry { return s.actions }

// Services exposes the service registry.
func (s *Server) Services() *service.Registry { return s.services }

// pluginLoadTimeout bounds loading every installed plugin into a new host.
// Generous: a plugin's import may pull in a large ML framework.
const pluginLoadTimeout = 5 * time.Minute

// SetGraphQLHandler registers Stash's own API handler.
//
// Called by internal/api once the handler exists - the same pattern the plugin
// cache uses. It is what lets a plugin query Stash without any credential
// existing in the host process: the request is served in-process, never over a
// socket. Nothing else in this package may import internal/api, so the
// dependency travels in this direction only.
func (s *Server) SetGraphQLHandler(h http.Handler) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.graphQL = h
}

func (s *Server) graphQLHandler() http.Handler {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.graphQL
}

// Tagging returns the AI tagging service, or nil when not running.
func (s *Server) Tagging() *tagging.Service {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tagging
}

// Trainer returns the head trainer, or nil when not running.
func (s *Server) Trainer() *tagging.Trainer {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.trainer
}

// TaggingStatus reports whether analysis can run, and why not when it cannot.
func (s *Server) TaggingStatus() tagging.Status {
	if s == nil {
		return tagging.Status{}
	}
	s.mu.RLock()
	status, tagger := s.taggingStatus, s.tagging
	s.mu.RUnlock()
	if tagger != nil {
		status = status.Current(tagger.Provider())
	}
	return status
}

// Catalog returns the plugin catalog manager, or nil when not running.
//
// Implements the HTTP layer's pluginsBackend so the plugin endpoints can be
// served without this package importing them.
func (s *Server) Catalog() *catalog.Manager {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.catalog
}

// ReloadPlugins recycles the plugin host, reporting whether there was one.
func (s *Server) ReloadPlugins() bool {
	supervisor := s.PluginHost()
	if supervisor == nil {
		return false
	}
	supervisor.Restart()
	return true
}

// ServePluginRoute proxies a request to a plugin's own HTTP route.
func (s *Server) ServePluginRoute(w http.ResponseWriter, r *http.Request, rest string) bool {
	plugins := s.Plugins()
	if plugins == nil {
		return false
	}
	return plugins.ServeRoute(w, r, rest)
}

// Plugins returns the plugin manager, or nil when not running.
func (s *Server) Plugins() *pluginhost.Manager {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.plugins
}

// PluginHost returns the plugin host supervisor, or nil when not running.
func (s *Server) PluginHost() *host.Supervisor {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.pluginHost
}

// Recommenders exposes the recommender registry.
func (s *Server) Recommenders() *recommend.Registry { return s.recommenders }

// SceneFetcher hydrates scenes for recommenders, using Stash's own repository
// rather than the schema-guessing SQL the out-of-process server needed.
func (s *Server) SceneFetcher() *recommend.Fetcher { return recommend.NewFetcher(s.deps.Repo) }

// VoyageSegmentIndex returns the opt-in read-only video recommendation index.
func (s *Server) VoyageSegmentIndex() *recommend.VoyageSegmentIndex {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.voyageSegments
}

// Tasks returns the scheduler, or nil when not running.
func (s *Server) Tasks() *task.Manager {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tasks
}

// Interactions returns the interaction ingest service, or nil when not running.
func (s *Server) Interactions() *interactions.Service {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.interactions
}

// Ready implements httpapi.Backend.
func (s *Server) Ready() bool { return s.Enabled() }

// Version implements httpapi.Backend.
func (s *Server) Version() httpapi.VersionInfo {
	minVersion := FrontendMinVersion
	info := httpapi.VersionInfo{
		BackendVersion:     BackendVersion,
		FrontendMinVersion: &minVersion,
	}
	if db := s.DB(); db != nil {
		if v, err := db.SchemaVersion(context.Background()); err == nil {
			info.SchemaVersion = fmt.Sprintf("%04d", v)
		}
	}
	return info
}

// Start opens the AI database, applies migrations, and seeds system settings.
//
// It is a no-op when ai_enabled is false. Errors are returned for logging but
// are not fatal: the caller is expected to keep Stash running, and the server
// reports StateFailed so the UI can explain itself.
func (s *Server) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.deps.Config.GetAIEnabled() {
		s.state = StateDisabled
		s.err = nil
		return nil
	}
	if s.state == StateReady {
		return nil
	}

	path := s.deps.Config.GetAIDatabasePath()
	db, err := store.Open(ctx, path)
	if err != nil {
		s.state, s.err = StateFailed, err
		return fmt.Errorf("open AI database at %s: %w", path, err)
	}

	if err := db.SeedSystemSettings(ctx); err != nil {
		_ = db.Close()
		s.state, s.err = StateFailed, err
		return fmt.Errorf("seed AI settings: %w", err)
	}

	version, err := db.SchemaVersion(ctx)
	if err != nil {
		_ = db.Close()
		s.state, s.err = StateFailed, err
		return fmt.Errorf("read AI schema version: %w", err)
	}

	// The scheduler is created per start so a restart cannot inherit stale
	// queues or half-cancelled work.
	interval := time.Duration(s.deps.Config.GetAITaskLoopInterval() * float64(time.Second))
	s.tasks = task.NewManager(task.Options{
		LoopInterval: interval,
		Debug:        s.deps.Config.GetAITaskDebug(),
		Gate:         s.services,
		History:      historySink{db: db},
	})
	s.tasks.Start()

	s.interactions = interactions.NewService(db)

	// Tagging is assembled from configuration and degrades to a reported state
	// rather than an error: a missing model or an unreachable inference server
	// disables analysis and leaves the scheduler, the interactions pipeline and
	// the recommenders running.
	taxonomyCategories := effectiveTaxonomyCategories(s.deps.Config.GetAITaggingTaxonomyCategories())
	taggingSettings := tagging.Settings{
		Provider:              s.deps.Config.GetAITaggingProvider(),
		ServerURL:             s.deps.Config.GetAITaggingServerURL(),
		OpenAIKey:             s.deps.Config.GetAITaggingOpenAIKey(),
		ModelDir:              s.deps.Config.GetAITaggingModelDir(),
		RulesDir:              s.deps.Config.GetAITaggingRulesDir(),
		VLMModel:              s.deps.Config.GetAITaggingVLMModel(),
		VLMLabels:             s.deps.Config.GetAITaggingVLMLabels(),
		VLMGPULayers:          s.deps.Config.GetAITaggingVLMGPULayers(),
		VLMContext:            s.deps.Config.GetAITaggingVLMContext(),
		AnalyzeMode:           s.deps.Config.GetAITaggingAnalyzeMode(),
		VLMAcceptMode:         s.deps.Config.GetAITaggingVLMAcceptMode(),
		VLMVoyageAPIKey:       s.deps.Config.GetAITaggingVLMVoyageAPIKey(),
		VLMVoyageRerankModel:  s.deps.Config.GetAITaggingVLMVoyageRerankModel(),
		VLMVoyageRerankTopK:   s.deps.Config.GetAITaggingVLMVoyageRerankTopK(),
		VLMVoyageEndpoint:     s.deps.Config.GetAITaggingVLMVoyageEndpoint(),
		TaxonomyEndpoint:      s.deps.Config.GetAITaggingTaxonomyEndpoint(),
		TaxonomyAPIKey:        s.deps.Config.GetAITaggingTaxonomyAPIKey(),
		TaxonomyCategories:    taxonomyCategories,
		TaxonomyMaxCandidates: s.deps.Config.GetAITaggingTaxonomyMaxCandidates(),
		FFmpegPath:            s.deps.Config.GetFFMpegPath(),
		FrameInterval:         s.deps.Config.GetAITaggingFrameInterval(),
		Threshold:             s.deps.Config.GetAITaggingThreshold(),
		MaxSpanMergeSeconds:   s.deps.Config.GetAITaggingMaxSpanMerge(),
	}
	taxonomyClient := tagging.NewTaxonomyClient(taggingSettings)
	taggingSettings.TaxonomyClient = taxonomyClient
	provider, rules, taggingStatus := tagging.Build(ctx, taggingSettings)
	s.taggingStatus = taggingStatus
	s.voyageSegments = nil
	if s.deps.Config.GetAITaggingVLMVoyageVideoEnabled() &&
		s.deps.Config.GetAITaggingVLMVoyageAPIKey() != "" {
		s.voyageSegments = &recommend.VoyageSegmentIndex{
			APIKey:      s.deps.Config.GetAITaggingVLMVoyageAPIKey(),
			Model:       s.deps.Config.GetAITaggingVLMVoyageVideoModel(),
			Endpoint:    s.deps.Config.GetAITaggingVLMVoyageEmbeddingEndpoint(),
			SegmentSecs: s.deps.Config.GetAITaggingVLMVoyageSegmentSecs(),
			Dimension:   s.deps.Config.GetAITaggingVLMVoyageDimension(),
			DB:          db,
			FFmpegPath:  s.deps.Config.GetFFMpegPath(),
			Taxonomy:    taxonomyClient,
			Categories:  taggingSettings.TaxonomyCategories,
		}
	}
	s.tagging = tagging.NewService(s.deps.Repo, db, tagging.Config{
		Provider:            provider,
		Rules:               rules,
		MaxSpanMergeSeconds: s.deps.Config.GetAITaggingMaxSpanMerge(),
		FFmpegPath:          s.deps.Config.GetFFMpegPath(),
		DefaultFrameInterval: tagging.DefaultFrameInterval(
			s.deps.Config.GetAITaggingProvider(), s.deps.Config.GetAITaggingFrameInterval()),
		SegmentIndexer:     s.voyageSegments,
		VoyageAnalyzer:     s.voyageSegments,
		TaxonomyClient:     taxonomyClient,
		TaxonomyCategories: taggingSettings.TaxonomyCategories,
	})
	s.trainer = tagging.NewTrainer(s.deps.Repo, db)

	// Registered whatever the provider's state: the action must be visible so
	// the user can see WHY it is unavailable when they try it, rather than the
	// button silently not existing.
	s.tagging.Register(s.actions, s.services)

	if taggingStatus.Available {
		logger.Infof("AI tagging ready: %s", taggingStatus.Message)
	} else if taggingStatus.Message != "" {
		logger.Debugf("AI tagging unavailable: %s", taggingStatus.Message)
	}

	// Plugin hosting is supervised independently: a Python environment that
	// cannot host plugins must not stop the rest of the AI server working.
	baseDir := filepath.Join(filepath.Dir(path), "ai")

	// The runtime is unpacked before the supervisor looks for it, so a fresh
	// install and an upgraded binary both find a current tree. A failure here
	// is reported and then ignored: the supervisor's own missing-runtime path
	// already degrades correctly.
	if version, err := pyhost.Extract(filepath.Join(baseDir, "runtime")); err != nil {
		logger.Errorf("could not unpack the AI plugin host runtime: %v", err)
	} else {
		logger.Debugf("AI plugin host runtime %s ready", version)
	}

	pluginDir := filepath.Join(baseDir, "plugins")

	// The plugin manager reads its collaborators through accessors rather than
	// holding them: the host process outlives an AI server restart, so the
	// database and scheduler it reaches must be the current ones.
	plugins := pluginhost.New(pluginhost.Deps{
		DB:        s.DB,
		Tasks:     s.Tasks,
		Actions:   s.actions,
		Services:  s.services,
		Recs:      s.recommenders,
		GraphQL:   s.graphQLHandler,
		PluginDir: pluginDir,
	}, nil)
	s.plugins = plugins

	s.pluginHost = host.New(host.Config{
		Enabled:          true,
		BaseDir:          baseDir,
		PluginDir:        pluginDir,
		ConfiguredPython: s.deps.Config.GetPythonPath(),
		Connect: func(ctx context.Context, address, token string) (host.Conn, error) {
			return hostconn.Dial(ctx, address, token, hostconn.Options{
				Bridge:     plugins.Bridge(),
				OnProgress: plugins.OnProgress(),
			})
		},
		OnReady: func(generation int) {
			// Loading runs off the supervisor's goroutine: importing a plugin
			// can take seconds, and blocking there would stall the health
			// checker that is meant to notice the host wedging.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), pluginLoadTimeout)
				defer cancel()
				if err := plugins.SyncFromHost(ctx); err != nil {
					logger.Errorf("could not load AI plugins: %v", err)
				}
			}()
		},
		OnGenerationEnd: plugins.HostCrashed,
	})
	plugins.SetSupervisor(s.pluginHost)

	// Catalog management is deliberately independent of the host: browsing,
	// planning, installing and removing are operations on the network and the
	// filesystem, and all of them work while the Python process is down.
	if err := db.SeedLocalSource(ctx); err != nil {
		logger.Errorf("could not seed the local plugin source: %v", err)
	}
	s.catalog = &catalog.Manager{
		DB:             s.DB,
		PluginDir:      pluginDir,
		BackendVersion: BackendVersion,
		Reload:         s.pluginHost.Restart,
	}
	s.pluginHost.Start()

	s.db = db
	s.state = StateReady
	s.err = nil

	logger.Infof("AI server ready (database %s, schema version %d)", path, version)
	return nil
}

// Shutdown releases resources. Safe to call when never started.
func (s *Server) Shutdown() {
	if s == nil {
		return
	}

	s.mu.Lock()
	tasks := s.tasks
	pluginHost := s.pluginHost
	plugins := s.plugins
	tagger := s.tagging
	db := s.db

	// Publish unready atomically with withdrawing the dependencies. Handlers
	// that gate on State can no longer enter while shutdown drains resources.
	if s.state != StateDisabled {
		s.state = StateStopped
	}
	s.db = nil
	s.tasks = nil
	s.interactions = nil
	s.pluginHost = nil
	s.plugins = nil
	s.catalog = nil
	s.tagging = nil
	s.trainer = nil
	s.voyageSegments = nil
	s.mu.Unlock()

	// Withdraw every registration before cancellation so no new task can enter
	// while the current handlers drain.
	s.actions.UnregisterService(tagging.ServiceName)
	s.services.Unregister(tagging.ServiceName)
	if plugins != nil {
		plugins.WithdrawAll()
	}

	// Cancel and wait before closing either the provider socket or the database.
	// Handlers are required to honor cancellation; closing resources under a
	// still-running handler turns a clean refresh into a use-after-close race.
	if tasks != nil {
		tasks.Shutdown(context.Background())
	}

	if tagger != nil {
		if err := tagger.Close(); err != nil {
			logger.Errorf("closing AI tagging provider: %v", err)
		}
	}

	if pluginHost != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pluginHost.Stop(ctx)
		cancel()
	}

	if db != nil {
		if err := db.Close(); err != nil {
			logger.Errorf("closing AI database: %v", err)
		}
	}
}

// Close releases everything including the HTTP layer. Used when Stash itself is
// shutting down, as opposed to the AI subsystem merely being disabled.
func (s *Server) Close() {
	if s == nil {
		return
	}
	s.Shutdown()
	s.api.Close()
}

// Refresh reacts to a configuration change: it stops the server and starts it
// again if still enabled. This is what makes toggling ai_enabled or changing
// ai_database_path take effect without restarting Stash.
//
// Serialized against other Refresh calls - see refreshMu - so two overlapping
// configuration changes run one full stop/start cycle at a time rather than
// interleaving.
func (s *Server) Refresh(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()

	s.Shutdown()
	return s.Start(ctx)
}

// State reports the current lifecycle state.
func (s *Server) State() State {
	if s == nil {
		return StateDisabled
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Err reports why the server is in StateFailed, or nil.
func (s *Server) Err() error {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.err
}

// Enabled reports whether the server is running and usable.
func (s *Server) Enabled() bool { return s.State() == StateReady }

// DB exposes the AI database, or nil when the server is not ready. Callers must
// handle nil rather than assuming availability.
func (s *Server) DB() *store.DB {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db
}

// Repo exposes Stash's repository to the AI subsystem's own packages.
func (s *Server) Repo() models.Repository { return s.deps.Repo }

func effectiveTaxonomyCategories(configured []string) []string {
	if len(configured) == 0 {
		return llamaprov.DefaultTaxonomyCategories()
	}
	return append([]string(nil), configured...)
}
