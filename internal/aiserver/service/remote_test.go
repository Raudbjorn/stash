package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock makes the readiness windows testable without sleeping through
// them: a 10-second cache and a 20-second backoff are not things to wait out.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// probeServer counts readiness probes and lets the test control the response.
type probeServer struct {
	*httptest.Server
	calls  atomic.Int64
	status atomic.Int64
	body   atomic.Value // string
}

func newProbeServer(t *testing.T) *probeServer {
	t.Helper()

	ps := &probeServer{}
	ps.status.Store(int64(http.StatusOK))
	ps.body.Store("")

	ps.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ps.calls.Add(1)
		if r.URL.Path != "/ready" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if ua := r.Header.Get("User-Agent"); ua != userAgent {
			t.Errorf("User-Agent = %q, want %q", ua, userAgent)
		}
		w.WriteHeader(int(ps.status.Load()))
		_, _ = w.Write([]byte(ps.body.Load().(string)))
	}))
	t.Cleanup(ps.Close)
	return ps
}

func newRemote(t *testing.T, url string, clock *fakeClock) *RemoteService {
	t.Helper()
	return &RemoteService{
		ServiceName: "svc",
		ServerURL:   url,
		Now:         clock.Now,
	}
}

// ------------------------------------------------------------------ tests ---

// A service with no server URL is local, always ready, and never probed.
func TestNoServerURLIsAlwaysReady(t *testing.T) {
	clock := newClock()
	s := newRemote(t, "", clock)

	if !s.EnsureReady(context.Background()) {
		t.Fatal("a service with no server URL should be ready")
	}
	if got := s.ConnectivityDetails().State; got != StateLocal {
		t.Errorf("state = %q, want %q", got, StateLocal)
	}
	if got := s.Connectivity(); got != "local: no server_url configured" {
		t.Errorf("connectivity = %q", got)
	}
}

// A recent success is trusted without re-probing; once the cache expires the
// service is probed again.
func TestReadinessSuccessIsCached(t *testing.T) {
	clock := newClock()
	ps := newProbeServer(t)
	s := newRemote(t, ps.URL, clock)
	ctx := context.Background()

	if !s.EnsureReady(ctx) {
		t.Fatal("first probe should succeed")
	}
	if ps.calls.Load() != 1 {
		t.Fatalf("probe calls = %d, want 1", ps.calls.Load())
	}

	// Repeated checks inside the cache window must not hit the network.
	clock.advance(9 * time.Second)
	for i := 0; i < 5; i++ {
		if !s.EnsureReady(ctx) {
			t.Fatal("cached readiness should still be true")
		}
	}
	if ps.calls.Load() != 1 {
		t.Errorf("probe calls = %d during the cache window, want 1", ps.calls.Load())
	}

	// Past the window, one more probe happens.
	clock.advance(2 * time.Second)
	if !s.EnsureReady(ctx) {
		t.Fatal("probe after cache expiry should succeed")
	}
	if ps.calls.Load() != 2 {
		t.Errorf("probe calls = %d after expiry, want 2", ps.calls.Load())
	}
}

// After a failure the service is not probed again until the backoff elapses,
// and reports "waiting" while suppressed.
func TestFailureBackoffSuppressesProbes(t *testing.T) {
	clock := newClock()
	ps := newProbeServer(t)
	ps.status.Store(int64(http.StatusServiceUnavailable))
	ps.body.Store("model still loading")

	s := newRemote(t, ps.URL, clock)
	ctx := context.Background()

	if s.EnsureReady(ctx) {
		t.Fatal("a 503 should not be ready")
	}
	if ps.calls.Load() != 1 {
		t.Fatalf("probe calls = %d, want 1", ps.calls.Load())
	}

	details := s.ConnectivityDetails()
	if details.State != StateUnreachable {
		t.Errorf("state = %q, want %q", details.State, StateUnreachable)
	}
	// The failing body is quoted so the user can see why.
	if !strings.Contains(details.Detail, "status-503") || !strings.Contains(details.Detail, "model still loading") {
		t.Errorf("detail = %q, want the status and body", details.Detail)
	}

	// Inside the backoff window: no further probes, and the state is "waiting".
	clock.advance(5 * time.Second)
	for i := 0; i < 3; i++ {
		if s.EnsureReady(ctx) {
			t.Fatal("should still be unready during backoff")
		}
	}
	if ps.calls.Load() != 1 {
		t.Errorf("probe calls = %d during backoff, want 1", ps.calls.Load())
	}
	if got := s.ConnectivityDetails().State; got != StateWaiting {
		t.Errorf("state during backoff = %q, want %q", got, StateWaiting)
	}
	// The detail still describes the real failure, which is the point: the
	// health endpoint keeps explaining why the service went away.
	if !strings.Contains(s.Connectivity(), "status-503") {
		t.Errorf("connectivity lost the failure reason: %q", s.Connectivity())
	}

	// Once the backoff elapses, probing resumes and recovery is detected.
	clock.advance(16 * time.Second)
	ps.status.Store(int64(http.StatusOK))
	ps.body.Store("")

	if !s.EnsureReady(ctx) {
		t.Fatal("service should be ready again after recovering")
	}
	if ps.calls.Load() != 2 {
		t.Errorf("probe calls = %d, want 2", ps.calls.Load())
	}
	if got := s.ConnectivityDetails().State; got != StateReady {
		t.Errorf("state = %q, want %q", got, StateReady)
	}
	// A success clears the failure bookkeeping.
	d := s.ConnectivityDetails()
	if d.LastReadyFailure != nil || d.LastReadyError != "" {
		t.Errorf("failure state survived a success: %+v", d)
	}
}

// Any status below 400 is a liveness signal, not an API contract.
func TestReadinessAcceptsAnyNon4xx(t *testing.T) {
	for _, status := range []int{200, 204, 301, 399} {
		clock := newClock()
		ps := newProbeServer(t)
		ps.status.Store(int64(status))
		s := newRemote(t, ps.URL, clock)

		if !s.EnsureReady(context.Background()) {
			t.Errorf("status %d should be treated as ready", status)
		}
	}
	for _, status := range []int{400, 404, 500, 503} {
		clock := newClock()
		ps := newProbeServer(t)
		ps.status.Store(int64(status))
		s := newRemote(t, ps.URL, clock)

		if s.EnsureReady(context.Background()) {
			t.Errorf("status %d should not be ready", status)
		}
	}
}

// An unreachable host is a network error, distinct from a bad status.
func TestUnreachableHostIsNetworkError(t *testing.T) {
	clock := newClock()
	// Port 1 on loopback refuses connections immediately.
	s := newRemote(t, "http://127.0.0.1:1", clock)
	s.ConnectTimeout = 200 * time.Millisecond

	if s.EnsureReady(context.Background()) {
		t.Fatal("an unreachable host should not be ready")
	}
	d := s.ConnectivityDetails()
	if d.State != StateUnreachable {
		t.Errorf("state = %q", d.State)
	}
	if !strings.Contains(d.Detail, "network-error") {
		t.Errorf("detail = %q, want a network error", d.Detail)
	}
}

// ForceReady bypasses both the success cache and the failure backoff, for the
// health endpoint where the user is asking "is it up right now?".
func TestForceReadyBypassesCaches(t *testing.T) {
	clock := newClock()
	ps := newProbeServer(t)
	s := newRemote(t, ps.URL, clock)
	ctx := context.Background()

	if !s.EnsureReady(ctx) {
		t.Fatal("first probe")
	}
	if !s.ForceReady(ctx) {
		t.Fatal("forced probe")
	}
	if ps.calls.Load() != 2 {
		t.Errorf("probe calls = %d, want 2 (force must bypass the success cache)", ps.calls.Load())
	}

	// Force also bypasses the failure backoff.
	ps.status.Store(int64(http.StatusServiceUnavailable))
	clock.advance(11 * time.Second)
	if s.EnsureReady(ctx) {
		t.Fatal("should fail")
	}
	before := ps.calls.Load()
	if s.ForceReady(ctx) {
		t.Fatal("should still fail")
	}
	if ps.calls.Load() != before+1 {
		t.Error("force did not bypass the failure backoff")
	}
}

// A burst of queued tasks must produce one probe, not one per task.
func TestConcurrentChecksProbeOnce(t *testing.T) {
	clock := newClock()
	ps := newProbeServer(t)
	s := newRemote(t, ps.URL, clock)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.EnsureReady(context.Background())
		}()
	}
	wg.Wait()

	if n := ps.calls.Load(); n != 1 {
		t.Errorf("probe calls = %d under concurrency, want 1", n)
	}
}

// The scheduler probes with a short timeout; a hanging server must not block a
// caller past its context deadline.
func TestProbeRespectsContextDeadline(t *testing.T) {
	clock := newClock()

	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		srv.Close()
	}()

	s := newRemote(t, srv.URL, clock)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	if s.EnsureReady(ctx) {
		t.Fatal("a hanging server should not report ready")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("probe took %v; it should honour the context deadline", elapsed)
	}
	if got := s.ConnectivityDetails().State; got != StateUnreachable {
		t.Errorf("state = %q", got)
	}
}

// ResetReadiness is called when the server URL setting changes.
func TestResetReadinessForcesFreshProbe(t *testing.T) {
	clock := newClock()
	ps := newProbeServer(t)
	s := newRemote(t, ps.URL, clock)
	ctx := context.Background()

	s.EnsureReady(ctx)
	s.ResetReadiness()

	if got := s.ConnectivityDetails().State; got != StateUnknown {
		t.Errorf("state after reset = %q, want %q", got, StateUnknown)
	}
	s.EnsureReady(ctx)
	if ps.calls.Load() != 2 {
		t.Errorf("probe calls = %d, want 2 (reset must clear the cache)", ps.calls.Load())
	}
}

// The rendered probe string is user-visible in the health endpoint.
func TestProbeDescribe(t *testing.T) {
	cases := []struct {
		name  string
		probe Probe
		want  string
	}{
		{"ready", Probe{OK: true, Status: "ready", StatusCode: 200, Latency: 12300 * time.Microsecond},
			"ready; status=200; 12.3ms"},
		{"bad status", Probe{Status: "status-503", StatusCode: 503, Error: "boom", Latency: 5 * time.Millisecond},
			"status-503; status=503; 5.0ms; error=boom"},
		{"network error", Probe{Status: "network-error", Error: "connection refused"},
			"network-error; error=connection refused"},
		{"bare", Probe{Status: "exception"}, "exception"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.probe.Describe(); got != tc.want {
				t.Errorf("Describe() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A long failing body is truncated so it stays readable in the UI.
func TestFailingBodyIsTruncated(t *testing.T) {
	clock := newClock()
	ps := newProbeServer(t)
	ps.status.Store(int64(http.StatusInternalServerError))
	ps.body.Store(strings.Repeat("x", 500))

	s := newRemote(t, ps.URL, clock)
	s.EnsureReady(context.Background())

	detail := s.ConnectivityDetails().Detail
	if !strings.Contains(detail, "...") {
		t.Errorf("long body was not truncated: %q", detail)
	}
	if len(detail) > 300 {
		t.Errorf("detail is %d chars, too long for display", len(detail))
	}
}

func TestJoinURL(t *testing.T) {
	cases := []struct{ base, path, want string }{
		{"http://host:8000", "/ready", "http://host:8000/ready"},
		{"http://host:8000/", "/ready", "http://host:8000/ready"},
		{"http://host:8000/", "ready", "http://host:8000/ready"},
		{"http://host:8000", "", "http://host:8000/"},
		// An absolute URL is used as-is.
		{"http://host:8000", "https://elsewhere/x", "https://elsewhere/x"},
	}
	for _, tc := range cases {
		if got := joinURL(tc.base, tc.path); got != tc.want {
			t.Errorf("joinURL(%q, %q) = %q, want %q", tc.base, tc.path, got, tc.want)
		}
	}
}

// Changing the server URL must rebuild the client rather than keep talking to
// the old host.
func TestClientRebuildsWhenURLChanges(t *testing.T) {
	s := &RemoteService{ServiceName: "svc", ServerURL: "http://a"}

	first := s.HTTPClient()
	if second := s.HTTPClient(); second != first {
		t.Error("client was rebuilt without a URL change")
	}

	s.ServerURL = "http://b"
	if third := s.HTTPClient(); third == first {
		t.Error("client was not rebuilt after the URL changed")
	}
}

func TestRemoteServiceSatisfiesServiceInterface(t *testing.T) {
	var _ Service = (*RemoteService)(nil)

	s := &RemoteService{ServiceName: "svc"}
	if s.Name() != "svc" {
		t.Errorf("Name() = %q", s.Name())
	}
	// An unset concurrency means serial, never zero.
	if s.MaxConcurrency() != 1 {
		t.Errorf("MaxConcurrency() = %d, want 1", s.MaxConcurrency())
	}
	s.Concurrency = 4
	if s.MaxConcurrency() != 4 {
		t.Errorf("MaxConcurrency() = %d, want 4", s.MaxConcurrency())
	}
}

// The registry is the scheduler's gate; unregistered services must not park
// their queues forever.
func TestRegistryGateDefaults(t *testing.T) {
	r := NewRegistry()

	if got := r.MaxConcurrency("unknown"); got != 1 {
		t.Errorf("unknown service concurrency = %d, want 1", got)
	}
	if !r.Ready(context.Background(), "unknown") {
		t.Error("an unregistered service should be treated as ready so its tasks fail for a real reason")
	}

	r.Register(&RemoteService{ServiceName: "svc", Concurrency: 3})
	if got := r.MaxConcurrency("svc"); got != 3 {
		t.Errorf("registered concurrency = %d, want 3", got)
	}
	if names := r.Names(); len(names) != 1 || names[0] != "svc" {
		t.Errorf("Names() = %v", names)
	}

	r.Unregister("svc")
	if _, ok := r.Get("svc"); ok {
		t.Error("service survived unregistration")
	}
}
