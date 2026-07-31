package aiserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/internal/manager/config"
)

// The settings UI reads specific keys out of the health snapshot. If any of
// these disappear the version display breaks and the component rows go blank,
// which is exactly the kind of regression a rename would cause silently.
func TestHealthSnapshotMatchesFrontendContract(t *testing.T) {
	cfg := newTestConfig(t, true)
	s := New(Deps{Config: cfg})
	ctx := context.Background()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Shutdown()

	raw, err := json.Marshal(s.Health(ctx))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// PluginSettings.tsx: interpretVersionPayload reads backend_version and
	// db_alembic_head; healthComponents renders stash_api and database.
	for _, key := range []string{"status", "timestamp", "stash_api", "database", "backend_version", "db_alembic_head"} {
		if _, ok := got[key]; !ok {
			t.Errorf("health snapshot missing %q, which the settings UI reads", key)
		}
	}

	if got["backend_version"] != BackendVersion {
		t.Errorf("backend_version = %v, want %q", got["backend_version"], BackendVersion)
	}
	// A four-digit string, whatever the current migration count: the frontend
	// only requires the key to be present and non-empty, so pinning the value
	// here would break on every schema addition for no benefit.
	head, _ := got["db_alembic_head"].(string)
	if len(head) != 4 {
		t.Errorf("db_alembic_head = %v, want a four-digit version", got["db_alembic_head"])
	}

	// Each component must be an object with a status the UI can switch on.
	for _, key := range []string{"stash_api", "database"} {
		comp, ok := got[key].(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %v", key, got[key])
		}
		status, _ := comp["status"].(string)
		switch HealthStatus(status) {
		case HealthOK, HealthWarn, HealthError:
		default:
			t.Errorf("%s.status = %q, want ok/warn/error", key, status)
		}
		if _, ok := comp["message"]; !ok {
			t.Errorf("%s missing message", key)
		}
	}
}

// A disabled server still answers with a well-formed snapshot, so the settings
// page can explain itself rather than showing a blank panel.
func TestHealthWhenDisabled(t *testing.T) {
	cfg := newTestConfig(t, false)
	s := New(Deps{Config: cfg})

	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	h := s.Health(context.Background())
	if h.Status != HealthWarn {
		t.Errorf("status = %q, want warn", h.Status)
	}
	if h.Database.Status != HealthWarn || h.Database.Message == "" {
		t.Errorf("database component = %+v, want a warning with an explanation", h.Database)
	}
	// Stash itself is fine; only the AI subsystem is off.
	if h.StashAPI.Status != HealthOK {
		t.Errorf("stash_api = %+v, want ok", h.StashAPI)
	}
	if h.BackendVersion == "" {
		t.Error("backend_version should be reported even while disabled")
	}
}

// An unreachable inference server degrades the snapshot to a warning without
// claiming the whole subsystem is broken.
func TestHealthReportsServiceConnectivity(t *testing.T) {
	cfg := newTestConfig(t, true)
	s := New(Deps{Config: cfg})
	ctx := context.Background()

	if err := s.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer s.Shutdown()

	// A healthy remote and a local one.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer up.Close()

	healthy := &service.RemoteService{ServiceName: "healthy", ServerURL: up.URL, Concurrency: 2}
	local := &service.RemoteService{ServiceName: "native", Concurrency: 3}
	s.Services().Register(healthy)
	s.Services().Register(local)

	// Prime their state; Health reports cached state and never probes.
	healthy.EnsureReady(ctx)
	local.EnsureReady(ctx)

	h := s.Health(ctx)
	if h.Status != HealthOK {
		t.Errorf("status = %q, want ok when every service is reachable", h.Status)
	}
	if got := h.Services["healthy"].State; got != string(service.StateReady) {
		t.Errorf("healthy service state = %q", got)
	}
	if got := h.Services["native"].State; got != string(service.StateLocal) {
		t.Errorf("local service state = %q, want local", got)
	}
	if got := h.Services["native"].Concurrency; got != 3 {
		t.Errorf("concurrency = %d, want 3", got)
	}

	// Now add a dead one.
	dead := &service.RemoteService{ServiceName: "dead", ServerURL: "http://127.0.0.1:1"}
	dead.ConnectTimeout = 200_000_000 // 200ms
	s.Services().Register(dead)
	dead.EnsureReady(ctx)

	h = s.Health(ctx)
	if h.Status != HealthWarn {
		t.Errorf("status = %q, want warn when a service is unreachable", h.Status)
	}
	// The database itself is still fine - only the remote is not.
	if h.Database.Status != HealthOK {
		t.Errorf("database = %+v, want ok", h.Database)
	}
	if got := h.Services["dead"].State; got != string(service.StateUnreachable) {
		t.Errorf("dead service state = %q, want unreachable", got)
	}
	if h.Services["dead"].Detail == "" {
		t.Error("an unreachable service should explain why")
	}
}

func TestPad4(t *testing.T) {
	for _, tc := range []struct {
		in   int
		want string
	}{{0, "0000"}, {1, "0001"}, {42, "0042"}, {1234, "1234"}} {
		if got := pad4(tc.in); got != tc.want {
			t.Errorf("pad4(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

var _ = config.AIEnabled
