package service

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// RemoteService is a service backed by an HTTP inference server.
//
// Most AI work is a long POST to a GPU box that may be starting up, busy, or
// absent. Rather than let the scheduler discover that per task, a readiness
// probe gates the whole service's queue, with a success cache so a healthy
// service is not re-probed constantly and a failure backoff so a dead one is
// not hammered.
type RemoteService struct {
	// ServiceName is the identifier used on actions and task records.
	ServiceName string

	// ServerURL is the base URL of the remote server. Empty means the service
	// is local (in-process) and always ready - it is not an error.
	//
	// Taken literally: the Python original rewrote "localhost" to
	// "host.docker.internal" when containerised, which has no meaning now that
	// this runs inside Stash.
	ServerURL string

	// ReadyEndpoint is probed to decide readiness. Defaults to "/ready".
	ReadyEndpoint string

	// Concurrency is how many of this service's tasks may run at once.
	Concurrency int

	// ReadinessCache is how long a success is trusted before re-probing.
	ReadinessCache time.Duration

	// FailureBackoff is how long to wait after a failure before probing again.
	FailureBackoff time.Duration

	// RequestTimeout bounds a whole request. The default is deliberately huge:
	// analysing a long video in one call is the normal case.
	RequestTimeout time.Duration

	// ConnectTimeout bounds establishing the connection, which is what
	// distinguishes "server is absent" from "server is working hard".
	ConnectTimeout time.Duration

	// Now is the clock, injectable for tests. Defaults to time.Now.
	Now func() time.Time

	// client is built lazily and rebuilt if ServerURL changes.
	clientMu  sync.Mutex
	client    *http.Client
	clientFor string

	// readyMu serialises probes so a burst of queued tasks produces one
	// request rather than one per task.
	readyMu sync.Mutex

	mu               sync.Mutex
	lastReadySuccess time.Time
	lastReadyFailure time.Time
	lastReadyAttempt time.Time
	nextReadyAttempt time.Time
	lastReadyError   string
	state            ConnectivityState
	detail           string
}

// ConnectivityState summarises what is known about the remote server.
type ConnectivityState string

const (
	// StateUnknown means no probe has run yet.
	StateUnknown ConnectivityState = "unknown"
	// StateLocal means there is no remote server to probe.
	StateLocal ConnectivityState = "local"
	// StateReady means the last probe succeeded.
	StateReady ConnectivityState = "ready"
	// StateWaiting means a probe is suppressed by the failure backoff.
	StateWaiting ConnectivityState = "waiting"
	// StateUnreachable means the last probe failed.
	StateUnreachable ConnectivityState = "unreachable"
)

// Defaults matching the Python service base.
const (
	defaultReadyEndpoint  = "/ready"
	defaultReadinessCache = 10 * time.Second
	defaultFailureBackoff = 20 * time.Second
	// Two hours: a single request may be an entire video analysis.
	defaultRequestTimeout = 2 * time.Hour
	defaultConnectTimeout = 10 * time.Second
	// userAgent is kept verbatim - remote servers may match on it.
	userAgent = "stash-ai-server-plugin/1.0"
	// errorBodyLimit caps how much of a failing response body is quoted.
	errorBodyLimit = 200
)

// Name implements Service.
func (s *RemoteService) Name() string { return s.ServiceName }

// MaxConcurrency implements Service.
func (s *RemoteService) MaxConcurrency() int {
	if s.Concurrency > 0 {
		return s.Concurrency
	}
	return 1
}

func (s *RemoteService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *RemoteService) readinessCache() time.Duration {
	if s.ReadinessCache > 0 {
		return s.ReadinessCache
	}
	return defaultReadinessCache
}

func (s *RemoteService) failureBackoff() time.Duration {
	if s.FailureBackoff > 0 {
		return s.FailureBackoff
	}
	return defaultFailureBackoff
}

func (s *RemoteService) readyEndpoint() string {
	if s.ReadyEndpoint != "" {
		return s.ReadyEndpoint
	}
	return defaultReadyEndpoint
}

// HTTPClient returns the shared client for this service, rebuilding it if the
// server URL has changed (a setting the user can edit at runtime).
func (s *RemoteService) HTTPClient() *http.Client {
	s.clientMu.Lock()
	defer s.clientMu.Unlock()

	if s.client != nil && s.clientFor == s.ServerURL {
		return s.client
	}

	requestTimeout := s.RequestTimeout
	if requestTimeout == 0 {
		requestTimeout = defaultRequestTimeout
	}
	connectTimeout := s.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = defaultConnectTimeout
	}

	s.client = &http.Client{
		Timeout: requestTimeout,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
			TLSHandshakeTimeout: connectTimeout,
		},
	}
	s.clientFor = s.ServerURL
	return s.client
}

// NewRequest builds a request against the service's base URL with the standard
// headers applied.
func (s *RemoteService) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if s.ServerURL == "" {
		return nil, fmt.Errorf("service %q does not define a server URL", s.ServiceName)
	}

	req, err := http.NewRequestWithContext(ctx, method, joinURL(s.ServerURL, path), body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	return req, nil
}

// joinURL appends a path to a base URL without doubling or dropping the slash.
// An absolute path argument is returned as-is, matching the original helper.
func joinURL(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	base = strings.TrimRight(base, "/")
	if path == "" {
		return base + "/"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return base + path
}

// Probe is the outcome of a readiness check.
type Probe struct {
	OK         bool
	Status     string
	StatusCode int
	Error      string
	Latency    time.Duration
}

// Describe renders the probe for display. The format is user-visible in the
// health endpoint: parts joined by "; ", e.g. "ready; status=200; 12.3ms".
func (p Probe) Describe() string {
	parts := []string{p.Status}
	if p.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status=%d", p.StatusCode))
	}
	if p.Latency != 0 {
		parts = append(parts, fmt.Sprintf("%.1fms", float64(p.Latency.Microseconds())/1000.0))
	}
	if p.Error != "" {
		parts = append(parts, "error="+p.Error)
	}
	return strings.Join(parts, "; ")
}

// CheckReady probes the readiness endpoint once, without consulting or updating
// any cached state.
//
// Any status below 400 counts as ready: the endpoint is a liveness signal, not
// an API, and servers vary in what they return.
func (s *RemoteService) CheckReady(ctx context.Context) Probe {
	start := s.now()

	req, err := s.NewRequest(ctx, http.MethodGet, s.readyEndpoint(), nil)
	if err != nil {
		return Probe{Status: "exception", Error: err.Error()}
	}

	resp, err := s.HTTPClient().Do(req)
	latency := s.now().Sub(start)
	if err != nil {
		return Probe{Status: "network-error", Error: err.Error(), Latency: latency}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 400 {
		// Drain so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, errorBodyLimit))
		return Probe{OK: true, Status: "ready", StatusCode: resp.StatusCode, Latency: latency}
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, errorBodyLimit*2))
	return Probe{
		Status:     fmt.Sprintf("status-%d", resp.StatusCode),
		StatusCode: resp.StatusCode,
		Error:      trim(string(body), errorBodyLimit),
		Latency:    latency,
	}
}

// trim shortens text to limit characters, marking truncation with an ellipsis.
func trim(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit-3] + "..."
}

// EnsureReady implements Service.
//
// The state machine, ported exactly:
//  1. No server URL: the service is local and always ready.
//  2. A success within ReadinessCache is trusted without probing.
//  3. While inside the failure backoff window the answer is a flat no, and the
//     state becomes "waiting".
//  4. Otherwise probe, and record success or failure.
//
// Callers get a bounded wait: the scheduler probes with a short timeout and
// treats a slow service as not ready rather than blocking its queue.
func (s *RemoteService) EnsureReady(ctx context.Context) bool {
	return s.ensureReady(ctx, false)
}

// ForceReady probes immediately, bypassing both the success cache and the
// failure backoff. Used by the health endpoint, where the user is explicitly
// asking "is it up right now?".
func (s *RemoteService) ForceReady(ctx context.Context) bool {
	return s.ensureReady(ctx, true)
}

func (s *RemoteService) ensureReady(ctx context.Context, force bool) bool {
	if s.ServerURL == "" {
		s.mu.Lock()
		s.state = StateLocal
		s.detail = "no server_url configured"
		s.mu.Unlock()
		return true
	}

	now := s.now()

	// Fast path: a recent success is trusted without taking the probe lock.
	s.mu.Lock()
	if !force && !s.lastReadySuccess.IsZero() && now.Sub(s.lastReadySuccess) < s.readinessCache() {
		s.mu.Unlock()
		return true
	}
	if !force && !s.nextReadyAttempt.IsZero() && now.Before(s.nextReadyAttempt) {
		// Note the asymmetry, preserved from the original: this sets the state
		// but leaves detail describing the last actual failure, so the health
		// endpoint keeps showing why the service went away.
		s.state = StateWaiting
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	s.readyMu.Lock()
	defer s.readyMu.Unlock()

	// Re-check under the probe lock: another caller may have just succeeded.
	now = s.now()
	s.mu.Lock()
	if !force && !s.lastReadySuccess.IsZero() && now.Sub(s.lastReadySuccess) < s.readinessCache() {
		s.mu.Unlock()
		return true
	}
	if !force && !s.lastReadyFailure.IsZero() && now.Sub(s.lastReadyFailure) < s.failureBackoff() {
		s.nextReadyAttempt = s.lastReadyFailure.Add(s.failureBackoff())
		s.state = StateUnreachable
		if s.lastReadyError != "" {
			s.detail = s.lastReadyError
		} else {
			s.detail = "service unreachable"
		}
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	probe := s.CheckReady(ctx)
	now = s.now()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastReadyAttempt = now

	if probe.OK {
		s.lastReadySuccess = now
		s.lastReadyFailure = time.Time{}
		s.nextReadyAttempt = time.Time{}
		s.lastReadyError = ""
		s.state = StateReady
		s.detail = probe.Describe()
		return true
	}

	s.lastReadyError = probe.Describe()
	s.lastReadyFailure = now
	s.nextReadyAttempt = now.Add(s.failureBackoff())
	s.state = StateUnreachable
	s.detail = probe.Describe()
	return false
}

// Connectivity renders the current state for display, as "state: detail" or
// just "state" when there is nothing to add.
func (s *RemoteService) Connectivity() string {
	s.mu.Lock()
	defer s.mu.Unlock()

	state := s.state
	if state == "" {
		state = StateUnknown
	}
	if s.detail != "" {
		return string(state) + ": " + s.detail
	}
	return string(state)
}

// ConnectivityDetails reports the full readiness state, for the health endpoint.
type ConnectivityDetails struct {
	State            ConnectivityState `json:"state"`
	Detail           string            `json:"detail,omitempty"`
	LastReadySuccess *time.Time        `json:"last_ready_success,omitempty"`
	LastReadyAttempt *time.Time        `json:"last_ready_attempt,omitempty"`
	LastReadyFailure *time.Time        `json:"last_ready_failure,omitempty"`
	LastReadyError   string            `json:"last_ready_error,omitempty"`
}

// ConnectivityDetails returns a snapshot of the readiness state.
func (s *RemoteService) ConnectivityDetails() ConnectivityDetails {
	s.mu.Lock()
	defer s.mu.Unlock()

	d := ConnectivityDetails{Detail: s.detail, LastReadyError: s.lastReadyError}
	d.State = s.state
	if d.State == "" {
		d.State = StateUnknown
	}
	if !s.lastReadySuccess.IsZero() {
		t := s.lastReadySuccess
		d.LastReadySuccess = &t
	}
	if !s.lastReadyAttempt.IsZero() {
		t := s.lastReadyAttempt
		d.LastReadyAttempt = &t
	}
	if !s.lastReadyFailure.IsZero() {
		t := s.lastReadyFailure
		d.LastReadyFailure = &t
	}
	return d
}

// ResetReadiness clears cached readiness so the next check probes immediately.
// Called when the server URL setting changes.
func (s *RemoteService) ResetReadiness() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastReadySuccess = time.Time{}
	s.lastReadyFailure = time.Time{}
	s.lastReadyAttempt = time.Time{}
	s.nextReadyAttempt = time.Time{}
	s.lastReadyError = ""
	s.state = StateUnknown
	s.detail = ""
}
