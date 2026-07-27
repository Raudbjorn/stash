package aiserver

import (
	"context"
	"time"

	"github.com/stashapp/stash/internal/aiserver/proc"
	"github.com/stashapp/stash/internal/aiserver/service"
)

// The health snapshot is a compatibility contract, not a free-form status
// object. PluginSettings.tsx reads specific fields out of it:
//
//   - backend_version and db_alembic_head drive the version display; without
//     them the UI reports "Backend did not report a version".
//   - stash_api and database are rendered as named rows, each expected to be
//     {status, message, ...} with status one of ok / warn / error.
//
// Anything else is additive and safely ignored by the frontend.

// HealthStatus is the per-component status the UI switches on.
type HealthStatus string

const (
	HealthOK    HealthStatus = "ok"
	HealthWarn  HealthStatus = "warn"
	HealthError HealthStatus = "error"
)

// HealthComponent is one named subsystem in the snapshot.
type HealthComponent struct {
	Status    HealthStatus   `json:"status"`
	Message   string         `json:"message"`
	Details   map[string]any `json:"details,omitempty"`
	LatencyMS *float64       `json:"latency_ms,omitempty"`
}

// Health is the payload of GET /api/v1/system/health.
type Health struct {
	Status    HealthStatus `json:"status"`
	Timestamp time.Time    `json:"timestamp"`

	// StashAPI reports reachability of Stash itself. In-process this cannot
	// fail - there is no URL, no API key and no network hop - so it is
	// reported as healthy with an explanation rather than dropped, because the
	// settings UI renders a row for it either way.
	StashAPI HealthComponent `json:"stash_api"`

	// Database is the AI server's own database.
	Database HealthComponent `json:"database"`

	BackendVersion string `json:"backend_version"`
	// SchemaVersion keeps the db_alembic_head key: there is no alembic any
	// more, but the frontend reads that name.
	SchemaVersion string `json:"db_alembic_head"`

	// State, Services and PluginHost are additions, ignored by the current
	// frontend but useful in the API and for diagnosing a stuck subsystem.
	State      State                    `json:"state"`
	Services   map[string]ServiceHealth `json:"services,omitempty"`
	PluginHost *PluginHostHealth        `json:"plugin_host,omitempty"`
	Error      string                   `json:"error,omitempty"`
}

// PluginHostHealth reports the Python plugin host's supervision state.
type PluginHostHealth struct {
	State       string `json:"state"`
	Message     string `json:"message,omitempty"`
	Remediation string `json:"remediation,omitempty"`
	Generation  int    `json:"generation"`
	Restarts    int    `json:"restarts"`

	// LastError and LastStderr describe the most recent failure.
	//
	// Without these the settings page can only say "restarting", which is the
	// least useful thing it could say: the stderr tail is where a Python
	// traceback or a bind failure actually appears, and it does not survive the
	// process that produced it.
	LastError  string   `json:"last_error,omitempty"`
	LastStderr []string `json:"last_stderr,omitempty"`
}

// ServiceHealth reports one registered service's connectivity.
type ServiceHealth struct {
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// Concurrency is how many of this service's tasks may run at once.
	Concurrency int `json:"concurrency"`
}

// Health probes the subsystem.
//
// In-process, the two things the Python server checked - can it reach Stash's
// API, and can it open Stash's database file - are no longer failure modes.
// What remains worth reporting is the AI database and each service's
// connectivity to its remote inference server.
func (s *Server) Health(ctx context.Context) Health {
	h := Health{
		Status:    HealthOK,
		Timestamp: time.Now().UTC(),
		State:     StateDisabled,
		StashAPI: HealthComponent{
			Status:  HealthOK,
			Message: "In-process; no network hop or API key required.",
		},
		BackendVersion: BackendVersion,
	}

	if s == nil {
		h.Status = HealthError
		h.Database = HealthComponent{Status: HealthError, Message: "AI server not initialised."}
		return h
	}

	s.mu.RLock()
	state, startErr, db := s.state, s.err, s.db
	s.mu.RUnlock()

	h.State = state
	if startErr != nil {
		h.Error = startErr.Error()
	}

	switch {
	case state == StateDisabled:
		h.Status = HealthWarn
		h.Database = HealthComponent{
			Status:  HealthWarn,
			Message: "The AI server is disabled. Enable it in Stash's configuration.",
		}
		return h

	case db == nil:
		h.Status = HealthError
		msg := "The AI database is not open."
		if startErr != nil {
			msg = startErr.Error()
		}
		h.Database = HealthComponent{Status: HealthError, Message: msg}
		return h
	}

	start := time.Now()
	pingErr := db.Ping(ctx)
	latency := float64(time.Since(start).Microseconds()) / 1000.0

	if pingErr != nil {
		h.Status = HealthError
		h.Database = HealthComponent{
			Status:    HealthError,
			Message:   pingErr.Error(),
			LatencyMS: &latency,
			Details:   map[string]any{"path": db.Path()},
		}
		return h
	}

	if v, err := db.SchemaVersion(ctx); err == nil {
		// Zero-padded to look like the migration filenames it replaces.
		h.SchemaVersion = pad4(v)
	}

	h.Database = HealthComponent{
		Status:    HealthOK,
		Message:   "Connected.",
		LatencyMS: &latency,
		Details:   map[string]any{"path": db.Path()},
	}

	h.Services = s.serviceHealth()

	// Plugin hosting degrades independently: a missing Python is a warning
	// about one feature, not a failure of the AI server.
	if hostSupervisor := s.PluginHost(); hostSupervisor != nil {
		status := hostSupervisor.Status()
		h.PluginHost = &PluginHostHealth{
			State:       string(status.State),
			Message:     status.Message,
			Remediation: status.Remediation,
			Generation:  status.Generation,
			Restarts:    status.Restarts,
		}
		if status.LastCrash != nil {
			h.PluginHost.LastError = status.LastCrash.Error
			// Bounded: the ring buffer holds 200 lines, and the last few are
			// what identify the failure.
			h.PluginHost.LastStderr = tailLines(status.LastCrash.Stderr, 20)
		}
		switch status.State {
		case proc.StateUnavailable, proc.StateFailed, proc.StateCrashed, proc.StateBackoff:
			h.Status = HealthWarn
		}
	}

	// A service that cannot reach its inference server is a warning, not an
	// error: the rest of the subsystem still works, and the tasks for that
	// service simply stay queued.
	for _, sh := range h.Services {
		if sh.State == string(service.StateUnreachable) || sh.State == string(service.StateWaiting) {
			h.Status = HealthWarn
			break
		}
	}

	return h
}

// serviceHealth collects connectivity for every registered service.
//
// It reports only cached state and never probes: the health endpoint is polled,
// and probing here would bypass the backoff that exists to stop a dead server
// being hammered.
func (s *Server) serviceHealth() map[string]ServiceHealth {
	names := s.services.Names()
	if len(names) == 0 {
		return nil
	}

	out := make(map[string]ServiceHealth, len(names))
	for _, name := range names {
		svc, ok := s.services.Get(name)
		if !ok {
			continue
		}

		entry := ServiceHealth{Concurrency: svc.MaxConcurrency()}
		if remote, isRemote := svc.(*service.RemoteService); isRemote {
			d := remote.ConnectivityDetails()
			entry.State = string(d.State)
			entry.Detail = d.Detail
		} else {
			entry.State = string(service.StateLocal)
		}
		out[name] = entry
	}
	return out
}

func pad4(v int) string {
	s := []byte("0000")
	i := len(s) - 1
	if v == 0 {
		return string(s)
	}
	for v > 0 && i >= 0 {
		s[i] = byte('0' + v%10)
		v /= 10
		i--
	}
	return string(s)
}

// HealthSnapshot implements httpapi.Backend, adapting the typed Health value to
// the untyped shape the HTTP layer serialises.
func (s *Server) HealthSnapshot(ctx context.Context) any { return s.Health(ctx) }

// tailLines returns the last n lines, or all of them when there are fewer.
func tailLines(lines []string, n int) []string {
	if len(lines) <= n {
		return lines
	}
	return lines[len(lines)-n:]
}
