package aiserver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
)

// newTestConfig builds a config rooted at a temp directory, so the AI database
// lands next to a throwaway config file rather than the user's.
func newTestConfig(t *testing.T, enabled bool) *config.Config {
	t.Helper()

	cfg := config.InitializeEmpty()
	cfg.SetConfigFile(filepath.Join(t.TempDir(), "config.yml"))
	cfg.SetBool(config.AIEnabled, enabled)
	return cfg
}

// The whole feature is opt-in: with ai_enabled false nothing starts and, most
// importantly, no database file is created.
func TestDisabledServerStartsNothing(t *testing.T) {
	cfg := newTestConfig(t, false)
	s := New(Deps{Config: cfg})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("Start while disabled returned an error: %v", err)
	}

	if got := s.State(); got != StateDisabled {
		t.Errorf("state = %q, want %q", got, StateDisabled)
	}
	if s.Enabled() {
		t.Error("Enabled() true while disabled")
	}
	if s.DB() != nil {
		t.Error("DB() should be nil while disabled")
	}
	if _, err := os.Stat(cfg.GetAIDatabasePath()); !os.IsNotExist(err) {
		t.Errorf("a disabled server created %s", cfg.GetAIDatabasePath())
	}

	// Shutting down something that never started must be safe.
	s.Shutdown()
}

func TestEnabledServerOpensAndMigrates(t *testing.T) {
	cfg := newTestConfig(t, true)
	s := New(Deps{Config: cfg})
	ctx := context.Background()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer s.Shutdown()

	if got := s.State(); got != StateReady {
		t.Fatalf("state = %q (err %v), want %q", got, s.Err(), StateReady)
	}
	if s.DB() == nil {
		t.Fatal("DB() is nil after a successful start")
	}
	if _, err := os.Stat(cfg.GetAIDatabasePath()); err != nil {
		t.Errorf("database file missing: %v", err)
	}

	// System settings are seeded on startup so the settings UI has rows to show.
	settings, err := s.DB().ListSettings(ctx, "__system__")
	if err != nil {
		t.Fatalf("list settings: %v", err)
	}
	if len(settings) == 0 {
		t.Error("no system settings were seeded")
	}

	// The schema version is read from the database rather than hard-coded, so
	// adding a migration does not break an assertion about readiness.
	version, err := s.DB().SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}

	h := s.Health(ctx)
	if h.State != StateReady {
		t.Errorf("health = %+v, want ready", h)
	}
	if want := fmt.Sprintf("%04d", version); h.SchemaVersion != want {
		t.Errorf("schema version = %q, want %q", h.SchemaVersion, want)
	}
	if h.Database.Status != HealthOK {
		t.Errorf("database component = %+v, want ok", h.Database)
	}
}

func TestShutdownWithdrawsReadinessAndDependencies(t *testing.T) {
	cfg := newTestConfig(t, true)
	s := New(Deps{Config: cfg})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldDB := s.DB()

	s.Shutdown()

	if got := s.State(); got != StateStopped {
		t.Fatalf("state = %q, want %q", got, StateStopped)
	}
	if s.Enabled() {
		t.Fatal("server remained enabled after shutdown")
	}
	if s.DB() != nil || s.Tasks() != nil || s.Interactions() != nil || s.PluginHost() != nil {
		t.Fatal("shutdown left a public dependency reachable")
	}
	if err := oldDB.Ping(context.Background()); err == nil {
		t.Fatal("withdrawn database remained open")
	}
}

func TestStartIsIdempotent(t *testing.T) {
	cfg := newTestConfig(t, true)
	s := New(Deps{Config: cfg})
	ctx := context.Background()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("first start: %v", err)
	}
	first := s.DB()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if s.DB() != first {
		t.Error("a second Start reopened the database instead of no-opping")
	}
	s.Shutdown()
}

// Toggling ai_enabled must take effect without restarting Stash.
func TestRefreshPicksUpConfigChange(t *testing.T) {
	cfg := newTestConfig(t, false)
	s := New(Deps{Config: cfg})
	ctx := context.Background()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("start disabled: %v", err)
	}
	if s.State() != StateDisabled {
		t.Fatalf("state = %q, want disabled", s.State())
	}

	cfg.SetBool(config.AIEnabled, true)
	if err := s.Refresh(ctx); err != nil {
		t.Fatalf("refresh after enabling: %v", err)
	}
	if s.State() != StateReady {
		t.Errorf("state = %q (err %v), want ready", s.State(), s.Err())
	}

	cfg.SetBool(config.AIEnabled, false)
	if err := s.Refresh(ctx); err != nil {
		t.Fatalf("refresh after disabling: %v", err)
	}
	if s.State() != StateDisabled {
		t.Errorf("state = %q, want disabled", s.State())
	}
	if s.DB() != nil {
		t.Error("DB() should be nil after disabling")
	}
}

// A broken database path must leave Stash running, with the reason visible.
func TestStartFailureIsReportedNotFatal(t *testing.T) {
	cfg := newTestConfig(t, true)

	// Point the database at a path whose parent is a regular file, so the
	// directory cannot be created.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg.SetString(config.AIDatabasePath, filepath.Join(blocker, "ai.db"))

	s := New(Deps{Config: cfg})
	err := s.Start(context.Background())
	if err == nil {
		t.Fatal("Start should have failed for an unusable database path")
	}
	if s.State() != StateFailed {
		t.Errorf("state = %q, want %q", s.State(), StateFailed)
	}
	if s.Err() == nil {
		t.Error("Err() is nil after a failed start")
	}

	h := s.Health(context.Background())
	if h.State != StateFailed || h.Error == "" {
		t.Errorf("health = %+v, want failed with a reason", h)
	}
	if h.Status != HealthError || h.Database.Status != HealthError {
		t.Errorf("a failed start should report error status: %+v", h)
	}
}

func TestNilServerIsSafe(t *testing.T) {
	// postInit and Shutdown run against whatever the manager holds; a nil
	// server must not panic.
	var s *Server
	if err := s.Start(context.Background()); err != nil {
		t.Errorf("nil Start: %v", err)
	}
	s.Shutdown()
	if s.State() != StateDisabled {
		t.Errorf("nil State() = %q", s.State())
	}
	if s.Enabled() {
		t.Error("nil Enabled() should be false")
	}
	if s.DB() != nil {
		t.Error("nil DB() should be nil")
	}
	if h := s.Health(context.Background()); h.State != StateDisabled {
		t.Errorf("nil Health() = %+v", h)
	}
}

func TestDatabasePathDefaultsBesideConfig(t *testing.T) {
	cfg := newTestConfig(t, true)

	want := filepath.Join(cfg.GetConfigPath(), "stash-ai.db")
	if got := cfg.GetAIDatabasePath(); got != want {
		t.Errorf("default database path = %q, want %q", got, want)
	}

	// A relative override resolves against the config directory.
	cfg.SetString(config.AIDatabasePath, "sub/ai.db")
	want = filepath.Join(cfg.GetConfigPath(), "sub/ai.db")
	if got := cfg.GetAIDatabasePath(); got != want {
		t.Errorf("relative override = %q, want %q", got, want)
	}

	// An absolute override is taken literally.
	abs := filepath.Join(t.TempDir(), "elsewhere.db")
	cfg.SetString(config.AIDatabasePath, abs)
	if got := cfg.GetAIDatabasePath(); got != abs {
		t.Errorf("absolute override = %q, want %q", got, abs)
	}
}
