package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// fakeSource serves a plugins_index.json and the files it advertises.
//
// Files are served from a map rather than disk so a test can publish a wrong
// checksum or a hostile path without having to create one.
type fakeSource struct {
	index Index
	files map[string][]byte
}

func (f *fakeSource) start(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/"+indexFile, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.index)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		data, ok := f.files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// newManager stands up a manager against a temporary database and plugin dir.
func newManager(t *testing.T, sourceURL string) (*Manager, *store.DB, string) {
	t.Helper()

	base := t.TempDir()
	// Deliberately NOT created: a fresh Stash has no plugin directory until the
	// first install, and that is exactly the path this must work on.
	pluginDir := filepath.Join(base, "plugins")

	db, err := store.Open(context.Background(), filepath.Join(base, "ai.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.SeedLocalSource(context.Background()); err != nil {
		t.Fatal(err)
	}
	if sourceURL != "" {
		if _, err := db.UpsertPluginSource(context.Background(), "test", sourceURL, true); err != nil {
			t.Fatal(err)
		}
	}

	return &Manager{
		DB:             func() *store.DB { return db },
		PluginDir:      pluginDir,
		BackendVersion: "0.9.3",
		// httptest serves plaintext on 127.0.0.1, which the loopback exemption
		// allows - exactly the case that exemption exists for.
		Client: &Client{HTTP: http.DefaultClient},
	}, db, pluginDir
}

const demoManifest = "name: demo\nversion: 1.2.0\nrequired_backend: \">=0.9.0\"\nfiles:\n  - plugin\n"

func demoSource() *fakeSource {
	manifest := []byte(demoManifest)
	code := []byte("VALUE = 1\n")

	return &fakeSource{
		index: Index{
			SchemaVersion: IndexSchemaVersion,
			Plugins: []IndexEntry{{
				Name:            "demo",
				Version:         "1.2.0",
				Description:     "A demo plugin",
				HumanName:       "Demo",
				RequiredBackend: ">=0.9.0",
				Files: []IndexFile{
					{Path: "plugin.yml", SHA256: digest(manifest)},
					{Path: "plugin.py", SHA256: digest(code)},
				},
			}},
		},
		files: map[string][]byte{
			"demo/plugin.yml": manifest,
			"demo/plugin.py":  code,
		},
	}
}

func TestRefreshStoresTheCatalog(t *testing.T) {
	source := demoSource()
	server := source.start(t)

	manager, db, _ := newManager(t, server.URL)
	ctx := context.Background()

	result, err := manager.Refresh(ctx, "test")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("refresh reported errors: %v", result.Errors)
	}
	if result.Fetched != 1 {
		t.Errorf("fetched = %d, want 1", result.Fetched)
	}

	row, err := db.GetPluginSource(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if row.LastRefreshedAt == nil {
		t.Error("the refresh time was not recorded")
	}
	if row.LastError != nil {
		t.Errorf("last_error = %v, want nil", *row.LastError)
	}

	entries, err := db.ListCatalog(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].PluginName != "demo" {
		t.Fatalf("catalog = %+v, want one demo entry", entries)
	}
}

// A source whose index this build cannot read must say so rather than silently
// storing an empty catalog that looks like "nothing available".
func TestRefreshReportsASchemaMismatch(t *testing.T) {
	source := demoSource()
	source.index.SchemaVersion = IndexSchemaVersion + 99
	server := source.start(t)

	manager, db, _ := newManager(t, server.URL)
	ctx := context.Background()

	result, err := manager.Refresh(ctx, "test")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if len(result.Errors) == 0 {
		t.Fatal("a schema mismatch was not reported")
	}

	row, _ := db.GetPluginSource(ctx, "test")
	if row.LastError == nil {
		t.Error("the failure was not recorded on the source")
	}
}

func TestInstallWritesAndRecordsThePlugin(t *testing.T) {
	source := demoSource()
	server := source.start(t)

	manager, db, pluginDir := newManager(t, server.URL)
	ctx := context.Background()

	if _, err := manager.Refresh(ctx, "test"); err != nil {
		t.Fatal(err)
	}

	result, err := manager.Install(ctx, "demo", "test", false, false)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if result.Status != "installed" {
		t.Errorf("status = %q", result.Status)
	}
	if len(result.Unverified) != 0 {
		t.Errorf("a checksummed install was reported unverified: %v", result.Unverified)
	}

	for _, name := range []string{"plugin.yml", "plugin.py"} {
		if _, err := os.Stat(filepath.Join(pluginDir, "demo", name)); err != nil {
			t.Errorf("%s was not written: %v", name, err)
		}
	}

	meta, err := db.GetPluginMeta(ctx, "demo")
	if err != nil {
		t.Fatalf("GetPluginMeta: %v", err)
	}
	if meta.Version != "1.2.0" {
		t.Errorf("version = %q, want 1.2.0", meta.Version)
	}
	if meta.Status != store.PluginStatusActive {
		t.Errorf("status = %q, want active", meta.Status)
	}

	// A second install must refuse rather than silently replacing files the
	// user may have edited.
	if _, err := manager.Install(ctx, "demo", "test", false, false); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("second install = %v, want ErrAlreadyInstalled", err)
	}

	// With overwrite it proceeds.
	if _, err := manager.Install(ctx, "demo", "test", true, false); err != nil {
		t.Errorf("overwriting install: %v", err)
	}
}

// The checksum is the whole point of the verified path: a file that does not
// match must never reach the plugin directory.
func TestInstallRejectsATamperedFile(t *testing.T) {
	source := demoSource()
	source.files["demo/plugin.py"] = []byte("import os; os.system('rm -rf /')\n")
	server := source.start(t)

	manager, _, pluginDir := newManager(t, server.URL)
	ctx := context.Background()

	if _, err := manager.Refresh(ctx, "test"); err != nil {
		t.Fatal(err)
	}

	_, err := manager.Install(ctx, "demo", "test", false, false)
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Install = %v, want ErrDigestMismatch", err)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "demo")); !os.IsNotExist(err) {
		t.Error("a plugin directory was created despite the checksum failure")
	}
}

// A catalog entry naming a path outside its directory must be refused before
// anything is written.
func TestDownloadRejectsPathTraversal(t *testing.T) {
	cases := []string{
		"../evil.py",
		"../../etc/passwd",
		"a/../../b.py",
		"/absolute.py",
	}

	for _, raw := range cases {
		t.Run(raw, func(t *testing.T) {
			if _, err := safeRelPath(raw); !errors.Is(err, ErrUnsafePath) {
				t.Errorf("safeRelPath(%q) = %v, want ErrUnsafePath", raw, err)
			}
		})
	}

	// And a legitimate nested path must still be allowed.
	if got, err := safeRelPath("sub/dir/plugin.py"); err != nil || got != "sub/dir/plugin.py" {
		t.Errorf("safeRelPath rejected a legitimate path: %q, %v", got, err)
	}
}

// WriteFiles is the last gate before bytes hit the disk, so it re-checks rather
// than trusting the download stage.
func TestWriteFilesRefusesEscapingPaths(t *testing.T) {
	dir := t.TempDir()

	err := WriteFiles(dir, []DownloadedFile{{RelPath: "../escaped.py", Data: []byte("x")}})
	if !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("WriteFiles = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.py")); err == nil {
		t.Fatal("a file was written outside the destination directory")
	}
}

// Plugin code executes on the user's machine, so a plaintext source is refused
// unless it is the developer's own loopback.
func TestSourceURLValidation(t *testing.T) {
	cases := []struct {
		url     string
		wantErr bool
	}{
		{"https://example.com/catalog", false},
		{"https://raw.githubusercontent.com/a/b/main", false},
		{"http://localhost:8080/catalog", false},
		{"http://127.0.0.1:8080/catalog", false},
		{"http://example.com/catalog", true},
		{"ftp://example.com/catalog", true},
		{"file:///etc", true},
	}

	for _, tc := range cases {
		t.Run(tc.url, func(t *testing.T) {
			err := ValidateSourceURL(tc.url)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateSourceURL accepted %q", tc.url)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateSourceURL rejected %q: %v", tc.url, err)
			}
		})
	}
}

// Dependencies must be an explicit choice, because installing something the
// user did not name is exactly the surprise the plan step exists to prevent.
func TestInstallRequiresConsentForDependencies(t *testing.T) {
	base := []byte("name: base\nversion: 1.0.0\nfiles:\n  - plugin\n")
	baseCode := []byte("BASE = 1\n")
	child := []byte("name: child\nversion: 1.0.0\ndependsOn:\n  - base\nfiles:\n  - plugin\n")
	childCode := []byte("CHILD = 1\n")

	source := &fakeSource{
		index: Index{
			SchemaVersion: IndexSchemaVersion,
			Plugins: []IndexEntry{
				{Name: "base", Version: "1.0.0", Files: []IndexFile{
					{Path: "plugin.yml", SHA256: digest(base)},
					{Path: "plugin.py", SHA256: digest(baseCode)},
				}},
				{Name: "child", Version: "1.0.0", DependsOn: []any{"base"}, Files: []IndexFile{
					{Path: "plugin.yml", SHA256: digest(child)},
					{Path: "plugin.py", SHA256: digest(childCode)},
				}},
			},
		},
		files: map[string][]byte{
			"base/plugin.yml":  base,
			"base/plugin.py":   baseCode,
			"child/plugin.yml": child,
			"child/plugin.py":  childCode,
		},
	}
	server := source.start(t)

	manager, db, _ := newManager(t, server.URL)
	ctx := context.Background()

	if _, err := manager.Refresh(ctx, "test"); err != nil {
		t.Fatal(err)
	}

	_, err := manager.Install(ctx, "child", "test", false, false)
	var needsDeps *DependenciesRequiredError
	if !errors.As(err, &needsDeps) {
		t.Fatalf("Install = %v, want DependenciesRequiredError", err)
	}
	if len(needsDeps.Dependencies) != 1 || needsDeps.Dependencies[0] != "base" {
		t.Errorf("dependencies = %v, want [base]", needsDeps.Dependencies)
	}

	result, err := manager.Install(ctx, "child", "test", false, true)
	if err != nil {
		t.Fatalf("Install with consent: %v", err)
	}
	if len(result.Installed) != 2 {
		t.Errorf("installed %v, want both base and child", result.Installed)
	}
	// The dependency must land before the plugin that needs it.
	if result.Installed[0][0] != "base" {
		t.Errorf("install order = %v, want base first", result.Installed)
	}

	if _, err := db.GetPluginMeta(ctx, "base"); err != nil {
		t.Errorf("the dependency was not recorded: %v", err)
	}
}

// Removing something another plugin needs must be a decision, not a side
// effect: the dependent could not load afterwards and would fail obscurely.
func TestRemoveRefusesToOrphanDependents(t *testing.T) {
	manager, db, pluginDir := newManager(t, "")
	ctx := context.Background()

	writeInstalled(t, pluginDir, "base", "name: base\nversion: 1.0.0\n")
	writeInstalled(t, pluginDir, "child", "name: child\nversion: 1.0.0\ndependsOn:\n  - base\n")

	for _, name := range []string{"base", "child"} {
		if err := db.UpsertPluginMeta(ctx, store.PluginMeta{
			Name: name, Version: "1.0.0", Status: store.PluginStatusActive,
		}); err != nil {
			t.Fatal(err)
		}
	}

	_, err := manager.Remove(ctx, "base", false, false)
	var dependents *DependentsError
	if !errors.As(err, &dependents) {
		t.Fatalf("Remove = %v, want DependentsError", err)
	}
	if len(dependents.Dependents) != 1 || dependents.Dependents[0] != "child" {
		t.Errorf("dependents = %v, want [child]", dependents.Dependents)
	}

	// Nothing may have been removed by the refused call.
	if _, err := os.Stat(filepath.Join(pluginDir, "base")); err != nil {
		t.Error("a refused removal deleted files anyway")
	}

	result, err := manager.Remove(ctx, "base", true, false)
	if err != nil {
		t.Fatalf("cascading remove: %v", err)
	}
	if len(result.Removed) != 2 {
		t.Errorf("removed %v, want both", result.Removed)
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "child")); !os.IsNotExist(err) {
		t.Error("the dependent survived a cascading removal")
	}
}

// Removal without drop_data must keep the plugin's tables: a user reinstalling
// to upgrade does not expect to lose their results.
func TestRemoveKeepsDataUnlessAsked(t *testing.T) {
	manager, db, pluginDir := newManager(t, "")
	ctx := context.Background()

	writeInstalled(t, pluginDir, "demo", "name: demo\nversion: 1.0.0\n")
	if err := db.UpsertPluginMeta(ctx, store.PluginMeta{
		Name: "demo", Version: "1.0.0", Status: store.PluginStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx,
		`CREATE TABLE p_demo_items (id INTEGER PRIMARY KEY) STRICT`); err != nil {
		t.Fatal(err)
	}

	if _, err := manager.Remove(ctx, "demo", false, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := db.PluginQuery(ctx, "demo", "SELECT id FROM p_demo_items", nil); err != nil {
		t.Errorf("removal dropped the plugin's table without being asked: %v", err)
	}

	// The record is kept as a tombstone rather than deleted, so its settings
	// survive a reinstall.
	meta, err := db.GetPluginMeta(ctx, "demo")
	if err != nil {
		t.Fatalf("the plugin record was deleted: %v", err)
	}
	if meta.Status != store.PluginStatusRemoved {
		t.Errorf("status = %q, want removed", meta.Status)
	}

	// With drop_data it really does go.
	writeInstalled(t, pluginDir, "demo", "name: demo\nversion: 1.0.0\n")
	if _, err := manager.Remove(ctx, "demo", false, true); err != nil {
		t.Fatalf("Remove with drop_data: %v", err)
	}
	if _, err := db.PluginQuery(ctx, "demo", "SELECT id FROM p_demo_items", nil); err == nil {
		t.Error("drop_data left the plugin's table behind")
	}
}

func writeInstalled(t *testing.T, pluginDir, name, manifest string) {
	t.Helper()

	dir := filepath.Join(pluginDir, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.yml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

// A plugin whose manifest disagrees with the catalog about its own name would
// be installed under a directory its code does not expect.
func TestInstallRejectsAManifestNameMismatch(t *testing.T) {
	manifest := []byte("name: something-else\nversion: 1.0.0\n")
	source := &fakeSource{
		index: Index{
			SchemaVersion: IndexSchemaVersion,
			Plugins: []IndexEntry{{
				Name: "demo", Version: "1.0.0",
				Files: []IndexFile{{Path: "plugin.yml", SHA256: digest(manifest)}},
			}},
		},
		files: map[string][]byte{"demo/plugin.yml": manifest},
	}
	server := source.start(t)

	manager, _, pluginDir := newManager(t, server.URL)
	ctx := context.Background()

	if _, err := manager.Refresh(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Install(ctx, "demo", "test", false, false); err == nil {
		t.Fatal("Install accepted a manifest naming a different plugin")
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "demo")); !os.IsNotExist(err) {
		t.Error("the mismatched plugin was installed anyway")
	}
}
