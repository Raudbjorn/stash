// Package service holds the registry of AI services - the logical groups that
// own actions and constrain how much work runs at once.
//
// A service is usually a plugin talking to a remote inference server, but it
// may equally be a native in-process provider. The scheduler only cares about
// two things: how many of its tasks may run concurrently, and whether it is
// ready to accept work.
package service

import (
	"context"
	"sort"
	"sync"
)

// Service is what the scheduler needs from a service.
type Service interface {
	// Name is the service identifier used on actions and task records.
	Name() string

	// MaxConcurrency is how many of this service's tasks may run at once.
	// Values below 1 are treated as 1.
	MaxConcurrency() int

	// EnsureReady reports whether the service can accept work now.
	//
	// Implementations must respect ctx: the scheduler probes with a short
	// timeout and treats a slow service as not ready, rather than waiting.
	// A service with no remote dependency should simply return true.
	EnsureReady(ctx context.Context) bool
}

// Registry holds the registered services.
type Registry struct {
	mu       sync.RWMutex
	services map[string]Service
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{services: make(map[string]Service)}
}

// Register adds or replaces a service.
func (r *Registry) Register(s Service) {
	if s == nil || s.Name() == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.services[s.Name()] = s
}

// Unregister removes a service.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.services, name)
}

// Get returns a service by name.
func (r *Registry) Get(name string) (Service, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.services[name]
	return s, ok
}

// Names lists registered services in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.services))
	for name := range r.services {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// MaxConcurrency implements the scheduler's gate. An unregistered service gets
// a concurrency of 1, so tasks submitted before their service registers still
// run, just serially.
func (r *Registry) MaxConcurrency(name string) int {
	s, ok := r.Get(name)
	if !ok {
		return 1
	}
	if n := s.MaxConcurrency(); n > 0 {
		return n
	}
	return 1
}

// Ready implements the scheduler's gate. An unregistered service is treated as
// ready: its tasks fail for a real reason rather than silently parking forever.
func (r *Registry) Ready(ctx context.Context, name string) bool {
	s, ok := r.Get(name)
	if !ok {
		return true
	}
	return s.EnsureReady(ctx)
}

// Static is a Service with fixed properties, for native in-process providers
// that have nothing to probe.
type Static struct {
	ServiceName    string
	Concurrency    int
	ReadyFunc      func(ctx context.Context) bool
	DefaultToReady bool
}

// Name implements Service.
func (s *Static) Name() string { return s.ServiceName }

// MaxConcurrency implements Service.
func (s *Static) MaxConcurrency() int {
	if s.Concurrency > 0 {
		return s.Concurrency
	}
	return 1
}

// EnsureReady implements Service.
func (s *Static) EnsureReady(ctx context.Context) bool {
	if s.ReadyFunc != nil {
		return s.ReadyFunc(ctx)
	}
	return true
}
