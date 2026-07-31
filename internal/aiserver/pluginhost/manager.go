package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/host"
	"github.com/stashapp/stash/internal/aiserver/hostconn"
	"github.com/stashapp/stash/internal/aiserver/plugin"
	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/task"
	"github.com/stashapp/stash/pkg/logger"
)

// Deps are the collaborators the manager publishes into and reads from.
//
// They are supplied as accessors rather than values because the AI server can
// be stopped and restarted - swapping the database and scheduler - while the
// host process and its loaded plugins carry on.
type Deps struct {
	DB       func() *store.DB
	Tasks    func() *task.Manager
	Actions  *action.Registry
	Services *service.Registry
	Recs     *recommend.Registry

	// GraphQL is Stash's own API handler, registered once it exists.
	GraphQL func() http.Handler

	// PluginDir is where installed plugins live.
	PluginDir string
}

// Manager loads plugins into the host and publishes what they register.
type Manager struct {
	deps Deps
	sup  *host.Supervisor

	mu sync.RWMutex
	// loaded records what each plugin registered, so unloading can withdraw
	// exactly that and nothing else.
	loaded map[string]loadedPlugin
	// generation is the host generation the registrations belong to. A crash
	// invalidates them all at once.
	generation int

	// inflight tracks running invocations so a crash can fail them terminally
	// rather than leaving the UI's progress bars spinning forever.
	inflight map[string]*invocation
}

// loadedPlugin is what one plugin contributed.
type loadedPlugin struct {
	Name         string
	Actions      []string
	Recommenders []string
	Services     []string
	// Routes counts the HTTP routes the plugin registered, which is all Go
	// needs: the patterns and handlers stay in the host.
	Routes   int
	Status   string
	Error    string
	LoadedAt time.Time
}

// invocation is one in-flight call into the host.
type invocation struct {
	plugin     string
	actionID   string
	generation int
	cancel     context.CancelCauseFunc
	// handle is the running task, so a plugin can declare itself a controller
	// from inside its own handler. Nil for recommenders, which are synchronous
	// requests rather than scheduled tasks.
	handle action.Handle
}

// ErrHostUnavailable is returned when the plugin host is not accepting work.
//
// Distinct from a plugin error on purpose: the UI says "the plugin host is
// restarting" rather than blaming the plugin.
var ErrHostUnavailable = errors.New("the plugin host is not running")

// New builds a manager.
func New(deps Deps, sup *host.Supervisor) *Manager {
	return &Manager{
		deps:     deps,
		sup:      sup,
		loaded:   make(map[string]loadedPlugin),
		inflight: make(map[string]*invocation),
	}
}

// SetSupervisor attaches the supervisor.
//
// Separate from New because the two are mutually referential: the supervisor's
// Connect callback needs this manager's Bridge, and this manager needs the
// supervisor to reach the live connection.
func (m *Manager) SetSupervisor(sup *host.Supervisor) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sup = sup
}

func (m *Manager) supervisor() *host.Supervisor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sup
}

// markRunning records which plugin is executing, so a crash names a culprit.
func (m *Manager) markRunning(name string) {
	if sup := m.supervisor(); sup != nil {
		sup.SetRunningPlugin(name)
	}
}

// restartHost recycles the host process.
func (m *Manager) restartHost() {
	if sup := m.supervisor(); sup != nil {
		sup.Restart()
	}
}

// Bridge returns the callback surface to hand to the transport.
func (m *Manager) Bridge() hostconn.Bridge { return &bridge{m: m} }

// OnProgress returns the progress callback to hand to the transport.
//
// Progress arrives asynchronously and is keyed by task id, so it lands on the
// scheduler's existing publish path and reaches the WebSocket unchanged - the
// frontend cannot tell a Python plugin's progress from a native provider's.
func (m *Manager) OnProgress() hostconn.ProgressFunc {
	return func(invocationID string, fraction float64, message string, detail map[string]any) {
		manager := m.deps.Tasks()
		if manager == nil {
			return
		}

		payload := map[string]any{}
		for k, v := range detail {
			payload[k] = v
		}
		// The two keys the shipped progress ring reads. Set after the detail
		// copy so a plugin cannot accidentally shadow them.
		payload["progress"] = fraction
		if message != "" {
			payload["message"] = message
		}

		manager.PublishProgress(invocationID, payload)
	}
}

// --------------------------------------------------------------- lifecycle --

// SyncFromHost loads every installed plugin into a freshly-ready host and
// publishes their registrations.
//
// Called on each new generation, including after a crash: the host holds no
// durable state, so a restart is only meaningful once its plugins are back.
func (m *Manager) SyncFromHost(ctx context.Context) error {
	conn, generation, err := m.connection()
	if err != nil {
		return err
	}

	manifests, problems := plugin.LoadManifests(m.deps.PluginDir)
	for _, problem := range problems {
		// A malformed manifest is the plugin author's problem, not a reason to
		// stop: the others still load.
		logger.Errorf("AI plugin manifest: %v", problem)
	}

	byName := make(map[string]plugin.Manifest, len(manifests))
	for _, manifest := range manifests {
		byName[manifest.Name] = manifest
	}

	// Load in dependency order so a plugin never imports a peer that has not
	// registered yet. Plugins in a dependency cycle are excluded rather than
	// loaded arbitrarily against a half-initialised peer.
	order, cycles := plugin.LoadOrder(manifests)
	for _, cycle := range cycles {
		logger.Errorf("AI plugin dependency problem: %v", cycle)
	}

	m.reset(generation)

	for _, name := range order {
		manifest, ok := byName[name]
		if !ok {
			continue
		}
		if err := m.loadOne(ctx, conn, generation, manifest); err != nil {
			// One plugin failing must not stop the others: that is the whole
			// reason Load reports a status rather than raising.
			logger.Errorf("AI plugin %s failed to load: %v", name, err)
		}
	}

	logger.Infof("AI plugin host generation %d: %d plugins loaded", generation, len(m.List()))
	return nil
}

func (m *Manager) loadOne(ctx context.Context, conn *hostconn.Conn, generation int, manifest plugin.Manifest) error {
	// Attribute a crash during import to the plugin that caused it.
	m.markRunning(manifest.Name)
	defer m.markRunning("")

	result, err := conn.Load(ctx, manifest.Name, manifest.Dir, manifest)
	if err != nil {
		return err
	}

	entry := loadedPlugin{
		Name:     manifest.Name,
		Status:   result.Status,
		Error:    result.Error,
		LoadedAt: time.Now(),
	}

	if result.Status != "ok" {
		m.mu.Lock()
		m.loaded[manifest.Name] = entry
		m.mu.Unlock()
		return errors.New(result.Error)
	}

	// Seed the plugin's declared settings so the UI can render them even while
	// the host is down.
	if db := m.deps.DB(); db != nil && len(manifest.Settings) > 0 {
		if err := db.SeedSettings(ctx, manifest.Name, settingDefs(manifest.Settings)); err != nil {
			logger.Errorf("could not seed settings for AI plugin %s: %v", manifest.Name, err)
		}
	}

	entry.Services = m.publishServices(manifest.Name, result.Registry.Services)
	entry.Actions = m.publishActions(generation, manifest.Name, result.Registry.Actions)
	entry.Recommenders = m.publishRecommenders(generation, manifest.Name, result.Registry.Recommenders)
	entry.Routes = countRoutes(manifest.Name, result.Registry.Routes)

	m.mu.Lock()
	m.loaded[manifest.Name] = entry
	m.mu.Unlock()
	return nil
}

// Unload withdraws a plugin's registrations and drops it from the host.
func (m *Manager) Unload(ctx context.Context, name string) error {
	m.withdraw(name)

	conn, _, err := m.connection()
	if err != nil {
		// Nothing to unload from: the registrations are already gone, which is
		// the observable part.
		return nil
	}
	return conn.Unload(ctx, name)
}

// Reload recycles the host, which is the safe way to apply a plugin change.
//
// Soft reimport leaves threads running and C-extension globals initialised;
// the host holds no durable state, so discarding it is both cheap and always
// correct.
func (m *Manager) Reload() { m.restartHost() }

// HostCrashed fails every in-flight invocation of a dead generation.
//
// Without this the UI waits forever: the task stays "running" with no process
// left to finish it. Failing terminally is deliberate - a half-applied side
// effect is worse to retry blindly than to report.
func (m *Manager) HostCrashed(generation int) {
	m.mu.Lock()
	var casualties []*invocation
	for id, inv := range m.inflight {
		if inv.generation != generation {
			continue
		}
		casualties = append(casualties, inv)
		delete(m.inflight, id)
	}
	m.mu.Unlock()

	for _, inv := range casualties {
		inv.cancel(fmt.Errorf("the plugin host stopped while %s was running", inv.actionID))
	}

	if len(casualties) > 0 {
		logger.Warnf("AI plugin host generation %d died with %d invocations in flight", generation, len(casualties))
	}
}

// reset clears registrations belonging to a previous generation.
func (m *Manager) reset(generation int) {
	previous := m.takeLoaded()
	m.mu.Lock()
	m.generation = generation
	m.mu.Unlock()

	for _, entry := range previous {
		m.withdrawRegistries(entry)
	}
}

// WithdrawAll removes every plugin registration.
//
// Called when the AI server stops: an action still in the registry after its
// host is gone would be offered in the UI and then fail on submission.
func (m *Manager) WithdrawAll() {
	for _, entry := range m.takeLoaded() {
		m.withdrawRegistries(entry)
	}
}

func (m *Manager) takeLoaded() map[string]loadedPlugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	previous := m.loaded
	m.loaded = make(map[string]loadedPlugin)
	return previous
}

func (m *Manager) withdraw(name string) {
	m.mu.Lock()
	entry, ok := m.loaded[name]
	delete(m.loaded, name)
	m.mu.Unlock()

	if ok {
		m.withdrawRegistries(entry)
	}
}

func (m *Manager) withdrawRegistries(entry loadedPlugin) {
	for _, svc := range entry.Services {
		m.deps.Actions.UnregisterService(svc)
		m.deps.Services.Unregister(svc)
	}
	if len(entry.Recommenders) > 0 {
		m.deps.Recs.UnregisterOwner(entry.Name)
	}
	// Actions registered under a service already went with it; anything left
	// used the plugin name as its service.
	m.deps.Actions.UnregisterService(entry.Name)
}

// List returns what is currently loaded, in name order.
func (m *Manager) List() []loadedPlugin {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]loadedPlugin, 0, len(m.loaded))
	for _, entry := range m.loaded {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ------------------------------------------------------------ registration --

func (m *Manager) publishServices(plugin string, entries []map[string]any) []string {
	var names []string
	for _, entry := range entries {
		spec, ok := decodeService(entry)
		if !ok {
			continue
		}
		m.deps.Services.Register(newPluginService(spec))
		names = append(names, spec.Name)
	}
	return names
}

func (m *Manager) publishActions(generation int, plugin string, entries []map[string]any) []string {
	var ids []string
	for _, entry := range entries {
		def, owner, ok := decodeAction(entry)
		if !ok {
			continue
		}
		if owner == "" {
			owner = plugin
		}
		m.deps.Actions.Register(def, m.actionHandler(generation, owner, def.ID))
		ids = append(ids, def.ID)
	}
	return ids
}

func (m *Manager) publishRecommenders(generation int, plugin string, entries []map[string]any) []string {
	var ids []string
	for _, entry := range entries {
		def, owner, ok := decodeRecommender(entry)
		if !ok {
			continue
		}
		if owner == "" {
			owner = plugin
		}
		if err := m.deps.Recs.Register(def, m.recommenderHandler(generation, owner, def.ID), owner); err != nil {
			logger.Errorf("AI plugin %s: %v", owner, err)
			continue
		}
		ids = append(ids, def.ID)
	}
	return ids
}

// pluginService adapts a host-declared service to the scheduler's interface.
//
// A plugin service that declares a server_url gets the same readiness FSM a
// native remote service does, so a hung inference server degrades only its own
// queue.
type pluginService struct {
	spec   serviceSpec
	remote *service.RemoteService
}

func newPluginService(spec serviceSpec) *pluginService {
	svc := &pluginService{spec: spec}
	if spec.ServerURL != "" {
		svc.remote = &service.RemoteService{
			ServiceName: spec.Name,
			ServerURL:   spec.ServerURL,
			Concurrency: spec.MaxConcurrency,
		}
	}
	return svc
}

func (s *pluginService) Name() string        { return s.spec.Name }
func (s *pluginService) MaxConcurrency() int { return s.spec.MaxConcurrency }

func (s *pluginService) EnsureReady(ctx context.Context) bool {
	if s.remote != nil {
		return s.remote.EnsureReady(ctx)
	}
	return true
}

// countRoutes counts the routes a plugin registered.
func countRoutes(plugin string, routes []map[string]any) int {
	count := 0
	for _, route := range routes {
		if owner, _ := route["plugin"].(string); owner == plugin {
			count++
		}
	}
	return count
}

// settingDefs converts manifest settings into the store's shape.
//
// Two nearly-identical structs, deliberately: the manifest package parses YAML
// written by plugin authors and the store package owns what is persisted.
// Coupling them would make a tolerated manifest alias into a schema change.
func settingDefs(defs []plugin.SettingDef) []store.SettingDef {
	out := make([]store.SettingDef, 0, len(defs))
	for _, def := range defs {
		out = append(out, store.SettingDef{
			Key:         def.Key,
			Type:        def.Type,
			Label:       def.Label,
			Description: def.Description,
			Default:     def.Default,
			Options:     def.Options,
		})
	}
	return out
}

// ------------------------------------------------------------- accessors ----

func (m *Manager) database() *store.DB       { return m.deps.DB() }
func (m *Manager) tasks() *task.Manager      { return m.deps.Tasks() }
func (m *Manager) actions() *action.Registry { return m.deps.Actions }

func (m *Manager) graphQL() http.Handler {
	if m.deps.GraphQL == nil {
		return nil
	}
	return m.deps.GraphQL()
}

// connection returns the live host connection and its generation.
func (m *Manager) connection() (*hostconn.Conn, int, error) {
	sup := m.supervisor()
	if sup == nil {
		return nil, 0, ErrHostUnavailable
	}
	raw := sup.Conn()
	if raw == nil {
		return nil, 0, ErrHostUnavailable
	}
	conn, ok := raw.(*hostconn.Conn)
	if !ok {
		return nil, 0, ErrHostUnavailable
	}
	return conn, sup.Status().Generation, nil
}
