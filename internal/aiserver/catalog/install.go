package catalog

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stashapp/stash/internal/aiserver/plugin"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/logger"
)

// The install lifecycle: refresh a source's index, plan, download, migrate,
// record. Every step happens in Go, so the whole of it works with the plugin
// host stopped - only activating the result needs Python.

// Manager owns the plugin catalog and the installed plugin directory.
type Manager struct {
	// DB accesses the AI database. Read through a function because it is
	// replaced when the AI server restarts.
	DB func() *store.DB
	// PluginDir is where plugins are installed.
	PluginDir string
	// BackendVersion gates required_backend constraints.
	BackendVersion string
	// Client fetches indexes and files.
	Client *Client
	// Reload recycles the plugin host to activate a change. Optional: without
	// it an install is recorded but not loaded until the next restart.
	Reload func()
}

// ErrAlreadyInstalled reports an install that would overwrite without asking.
var ErrAlreadyInstalled = errors.New("plugin is already installed")

// ErrInvalidPluginName rejects names that would alias another SQL namespace.
var ErrInvalidPluginName = errors.New("plugin name must contain only lowercase letters, digits, and underscores")

// RefreshResult summarises a catalog refresh.
//
// The field names match what the settings UI renders.
type RefreshResult struct {
	Source  string   `json:"source"`
	Fetched int      `json:"fetched"`
	Errors  []string `json:"errors"`
}

func (m *Manager) client() *Client {
	if m.Client != nil {
		return m.Client
	}
	return &Client{}
}

// Refresh fetches a source's index and replaces its catalog.
//
// Errors are collected rather than returned one at a time: a source with one
// unusable entry should still yield the rest, and the UI shows the list.
func (m *Manager) Refresh(ctx context.Context, sourceName string) (RefreshResult, error) {
	result := RefreshResult{Source: sourceName, Errors: []string{}}

	db := m.DB()
	if db == nil {
		return result, errors.New("the AI database is not available")
	}

	source, err := db.GetPluginSource(ctx, sourceName)
	if err != nil {
		return result, err
	}
	if source.Name == store.LocalSourceName {
		return result, store.ErrSourceImmutable
	}
	if !source.Enabled {
		return result, errors.New("SOURCE_DISABLED")
	}

	index, err := m.client().FetchIndex(ctx, source.URL)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		// Recorded on the source so the UI can explain a stale catalog without
		// the user having to retry to find out why.
		_ = db.MarkSourceRefreshed(ctx, source.ID, err.Error())
		return result, nil
	}

	entries := index.ToEntries(source.ID)
	if err := db.ReplaceCatalog(ctx, source.ID, entries); err != nil {
		result.Errors = append(result.Errors, err.Error())
		_ = db.MarkSourceRefreshed(ctx, source.ID, err.Error())
		return result, nil
	}

	result.Fetched = len(entries)
	_ = db.MarkSourceRefreshed(ctx, source.ID, "")
	return result, nil
}

// resolver adapts the database and plugin directory to plugin.Resolver.
type resolver struct {
	installed map[string]plugin.Manifest
	available map[string]plugin.Available
}

func (r *resolver) Installed(name string) (plugin.Manifest, bool) {
	manifest, ok := r.installed[name]
	return manifest, ok
}

func (r *resolver) InstalledNames() []string {
	out := make([]string, 0, len(r.installed))
	for name := range r.installed {
		out = append(out, name)
	}
	return out
}

func (r *resolver) Catalog(name string) (plugin.Available, bool) {
	entry, ok := r.available[name]
	return entry, ok
}

// newResolver builds planning inputs from disk and the catalog.
//
// preferredSource, when non-zero, wins where several sources advertise the same
// plugin - that is what the UI's source picker means.
func (m *Manager) newResolver(ctx context.Context, preferredSource int64) (*resolver, error) {
	db := m.DB()
	if db == nil {
		return nil, errors.New("the AI database is not available")
	}

	manifests, problems := plugin.LoadManifests(m.PluginDir)
	for _, problem := range problems {
		logger.Debugf("AI plugin manifest: %v", problem)
	}

	r := &resolver{
		installed: make(map[string]plugin.Manifest, len(manifests)),
		available: make(map[string]plugin.Available),
	}
	for _, manifest := range manifests {
		r.installed[manifest.Name] = manifest
	}

	sources, err := db.ListPluginSources(ctx)
	if err != nil {
		return nil, err
	}
	sourceNames := make(map[int64]string, len(sources))
	for _, source := range sources {
		sourceNames[source.ID] = source.Name
	}

	for _, source := range sources {
		entries, err := db.ListCatalog(ctx, source.ID)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			available := toAvailable(entry, sourceNames[entry.SourceID])

			// First writer wins unless the preferred source turns up later.
			if existing, seen := r.available[entry.PluginName]; seen {
				if preferredSource == 0 || entry.SourceID != preferredSource {
					continue
				}
				_ = existing
			}
			r.available[entry.PluginName] = available
		}
	}
	return r, nil
}

func toAvailable(entry store.CatalogEntry, sourceName string) plugin.Available {
	available := plugin.Available{
		Name:      entry.PluginName,
		Version:   entry.Version,
		DependsOn: entry.Dependencies,
		Source:    sourceName,
	}
	if entry.Description != nil {
		available.Description = *entry.Description
	}
	if entry.HumanName != nil {
		available.HumanName = *entry.HumanName
	}
	if raw, ok := entry.Manifest["required_backend"].(string); ok {
		available.RequiredBackend = raw
	}
	if raw, ok := entry.Manifest["path"].(string); ok {
		available.Path = raw
	}
	return available
}

// PlanInstall computes what installing a plugin would entail.
func (m *Manager) PlanInstall(ctx context.Context, name, sourceName string) (plugin.InstallPlan, error) {
	preferred, err := m.sourceID(ctx, sourceName)
	if err != nil {
		return plugin.InstallPlan{}, err
	}

	r, err := m.newResolver(ctx, preferred)
	if err != nil {
		return plugin.InstallPlan{}, err
	}
	if _, ok := r.available[name]; !ok {
		if _, installed := r.installed[name]; !installed {
			return plugin.InstallPlan{}, store.ErrNotFound
		}
	}
	return plugin.PlanInstall(r, name, m.BackendVersion), nil
}

// PlanRemove computes what removing a plugin would entail.
func (m *Manager) PlanRemove(ctx context.Context, name string) (plugin.RemovePlan, error) {
	r, err := m.newResolver(ctx, 0)
	if err != nil {
		return plugin.RemovePlan{}, err
	}
	if _, ok := r.installed[name]; !ok {
		return plugin.RemovePlan{}, store.ErrNotFound
	}
	return plugin.PlanRemove(r, name), nil
}

// InstallResult reports what an install actually did.
type InstallResult struct {
	Status string `json:"status"`
	Plugin string `json:"plugin"`
	// Installed lists every plugin installed, dependencies included, as
	// [name, version] pairs - the shape the existing UI renders.
	Installed [][2]string `json:"installed"`
	// Unverified names plugins whose files carried no published checksum, so
	// the user can see which of their plugins are trusted only by transport.
	Unverified []string `json:"unverified,omitempty"`
}

// Install downloads and installs a plugin and its dependencies.
//
// overwrite must be set to replace an existing install; installDependencies
// must be set to bring in anything the plugin needs. Both are explicit because
// both surprise a user who did not expect them.
func (m *Manager) Install(ctx context.Context, name, sourceName string, overwrite, installDependencies bool) (InstallResult, error) {
	result := InstallResult{Plugin: name, Status: "installed", Installed: [][2]string{}}
	if !store.ValidPluginIdentifier(name) {
		return result, fmt.Errorf("%w: %q", ErrInvalidPluginName, name)
	}

	plan, err := m.PlanInstall(ctx, name, sourceName)
	if err != nil {
		return result, err
	}
	if !plan.Executable() {
		return result, planError(plan)
	}

	// The plan omits what is already present, so a reinstall of an installed
	// plugin produces an empty Install list. Reporting success for doing
	// nothing would be a lie, so that case is answered explicitly.
	targets := append([]string(nil), plan.Install...)
	if contains(plan.AlreadyInstalled, name) {
		if !overwrite {
			return result, fmt.Errorf("%w: %s", ErrAlreadyInstalled, name)
		}
		targets = append(targets, name)
	}
	if len(targets) == 0 {
		return result, fmt.Errorf("%w: %s is in no catalog", store.ErrNotFound, name)
	}

	dependencies := make([]string, 0, len(targets))
	for _, target := range targets {
		if target != name {
			dependencies = append(dependencies, target)
		}
	}
	if len(dependencies) > 0 && !installDependencies {
		return result, &DependenciesRequiredError{Dependencies: dependencies}
	}

	for _, target := range targets {
		if !store.ValidPluginIdentifier(target) {
			return result, fmt.Errorf("%w: %q", ErrInvalidPluginName, target)
		}
	}

	preferred, err := m.sourceID(ctx, sourceName)
	if err != nil {
		return result, err
	}

	for _, target := range targets {
		// Dependencies always overwrite: the plan only lists them because they
		// are absent or stale, so a leftover directory is not a user's edit.
		version, unverified, err := m.installOne(ctx, target, preferred, overwrite || target != name)
		if err != nil {
			return result, err
		}
		result.Installed = append(result.Installed, [2]string{target, version})
		if unverified {
			result.Unverified = append(result.Unverified, target)
		}
	}

	if m.Reload != nil {
		m.Reload()
	}
	return result, nil
}

// installOne downloads and records a single plugin.
func (m *Manager) installOne(ctx context.Context, name string, preferredSource int64, overwrite bool) (string, bool, error) {
	db := m.DB()
	if db == nil {
		return "", false, errors.New("the AI database is not available")
	}
	if !store.ValidPluginIdentifier(name) {
		return "", false, fmt.Errorf("%w: %q", ErrInvalidPluginName, name)
	}

	entries, err := db.FindCatalogEntries(ctx, name)
	if err != nil {
		return "", false, err
	}
	if len(entries) == 0 {
		return "", false, fmt.Errorf("%w: %s is in no catalog", store.ErrNotFound, name)
	}

	entry := entries[0]
	for _, candidate := range entries {
		if preferredSource != 0 && candidate.SourceID == preferredSource {
			entry = candidate
			break
		}
	}

	sources, err := db.ListPluginSources(ctx)
	if err != nil {
		return "", false, err
	}
	var sourceURL string
	for _, source := range sources {
		if source.ID == entry.SourceID {
			sourceURL = source.URL
			break
		}
	}
	if sourceURL == "" {
		return "", false, fmt.Errorf("catalog entry for %s has no usable source", name)
	}

	target := filepath.Join(m.PluginDir, name)
	if _, err := os.Stat(target); err == nil && !overwrite {
		return "", false, fmt.Errorf("%w: %s", ErrAlreadyInstalled, name)
	}

	files, err := m.client().Download(ctx, sourceURL, entry)
	if err != nil {
		return "", false, fmt.Errorf("download %s: %w", name, err)
	}

	// On a fresh install nothing has created the plugin directory yet: the
	// supervisor passes it to the host as a search path without needing it to
	// exist, so the first install is where it comes into being.
	if err := os.MkdirAll(m.PluginDir, 0o755); err != nil {
		return "", false, fmt.Errorf("create the plugin directory: %w", err)
	}

	// Staged then swapped: a failed download must not leave a half-installed
	// plugin that the host would then try to import.
	staging, err := os.MkdirTemp(m.PluginDir, ".install-*")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(staging)

	if err := WriteFiles(staging, files); err != nil {
		return "", false, err
	}

	manifest, err := plugin.ParseManifestFile(filepath.Join(staging, "plugin.yml"))
	if err != nil {
		return "", false, fmt.Errorf("%s has no usable plugin.yml: %w", name, err)
	}
	if manifest.Name != "" && manifest.Name != name {
		// A mismatch means the catalog and the plugin disagree about identity,
		// which would install it under a name its own code does not expect.
		return "", false, fmt.Errorf("catalog calls this plugin %q but its manifest says %q", name, manifest.Name)
	}

	if err := os.RemoveAll(target); err != nil {
		return "", false, err
	}
	if err := os.Rename(staging, target); err != nil {
		return "", false, err
	}

	head := ""
	migrations, err := store.LoadPluginMigrations(filepath.Join(target, "migrations"))
	if err != nil {
		return "", false, fmt.Errorf("read migrations for %s: %w", name, err)
	}
	if len(migrations) > 0 {
		version, err := db.ApplyPluginMigrations(ctx, name, migrations)
		if err != nil {
			return "", false, err
		}
		head = fmt.Sprintf("%04d", version)
	}

	meta := store.PluginMeta{
		Name:            name,
		Version:         firstNonEmpty(manifest.Version, entry.Version, "0.0.0"),
		Status:          store.PluginStatusActive,
		RequiredBackend: manifest.RequiredBackend,
	}
	if head != "" {
		meta.MigrationHead = &head
	}
	if manifest.HumanName != "" {
		meta.HumanName = &manifest.HumanName
	} else {
		meta.HumanName = entry.HumanName
	}
	if manifest.ServerLink != "" {
		meta.ServerLink = &manifest.ServerLink
	} else {
		meta.ServerLink = entry.ServerLink
	}

	if err := db.UpsertPluginMeta(ctx, meta); err != nil {
		return "", false, err
	}

	unverified := false
	for _, file := range files {
		if !file.Verified {
			unverified = true
			break
		}
	}
	return meta.Version, unverified, nil
}

// RemoveResult reports what a removal did.
type RemoveResult struct {
	Status  string   `json:"status"`
	Plugin  string   `json:"plugin"`
	Removed []string `json:"removed"`
}

// Remove deletes a plugin, and its dependents when cascade is set.
//
// Dependents are removed rather than orphaned because a plugin whose dependency
// is gone cannot load, and would fail later with a much less obvious message.
func (m *Manager) Remove(ctx context.Context, name string, cascade, dropData bool) (RemoveResult, error) {
	result := RemoveResult{Status: "removed", Plugin: name, Removed: []string{}}

	plan, err := m.PlanRemove(ctx, name)
	if err != nil {
		return result, err
	}

	dependents := make([]string, 0, len(plan.Dependents))
	for _, dependent := range plan.Dependents {
		if dependent != name {
			dependents = append(dependents, dependent)
		}
	}
	if len(dependents) > 0 && !cascade {
		return result, &DependentsError{Dependents: dependents}
	}

	targets := plan.Remove
	if !cascade {
		targets = []string{name}
	}

	db := m.DB()
	for _, target := range targets {
		if err := os.RemoveAll(filepath.Join(m.PluginDir, target)); err != nil {
			return result, err
		}

		if db != nil {
			if dropData {
				// Only on request: a user removing a plugin to upgrade it does
				// not expect their results to go with it.
				if err := db.DropPluginTables(ctx, target); err != nil {
					return result, err
				}
				if err := db.DeletePluginMeta(ctx, target); err != nil {
					return result, err
				}
			} else if err := db.SetPluginStatus(ctx, target, store.PluginStatusRemoved, ""); err != nil &&
				!errors.Is(err, store.ErrNotFound) {
				return result, err
			}
		}

		result.Removed = append(result.Removed, target)
	}

	if m.Reload != nil {
		m.Reload()
	}
	return result, nil
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func (m *Manager) sourceID(ctx context.Context, sourceName string) (int64, error) {
	if sourceName == "" {
		return 0, nil
	}
	db := m.DB()
	if db == nil {
		return 0, errors.New("the AI database is not available")
	}
	source, err := db.GetPluginSource(ctx, sourceName)
	if err != nil {
		return 0, err
	}
	return source.ID, nil
}

// DependenciesRequiredError reports an install that needs confirmation.
type DependenciesRequiredError struct{ Dependencies []string }

func (e *DependenciesRequiredError) Error() string {
	return fmt.Sprintf("this plugin also needs %v", e.Dependencies)
}

// DependentsError reports a removal that would break other plugins.
type DependentsError struct{ Dependents []string }

func (e *DependentsError) Error() string {
	return fmt.Sprintf("other plugins depend on this one: %v", e.Dependents)
}

// planError turns an unexecutable plan into the most useful single error.
func planError(plan plugin.InstallPlan) error {
	if len(plan.Incompatible) > 0 {
		first := plan.Incompatible[0]
		return fmt.Errorf("%s requires backend %s, but this build is %s",
			first.Name, first.RequiredBackend, first.BackendVersion)
	}
	if len(plan.Missing) > 0 {
		return fmt.Errorf("no catalog provides %v", plan.Missing)
	}
	if len(plan.Errors) > 0 {
		return errors.New(plan.Errors[0])
	}
	return errors.New("the install plan cannot be carried out")
}
