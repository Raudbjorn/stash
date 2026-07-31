package action

import (
	"context"
	"sync"
)

// Handler executes an action.
//
// The Python original inspected each handler's arity to decide whether to pass
// the task record. Go has one signature instead: handlers that do not need the
// task handle simply ignore it. The handle is an interface so this package does
// not depend on the task scheduler, which depends on it.
type Handler func(ctx context.Context, in ContextInput, params map[string]any, task Handle) (any, error)

// Handle is the slice of the running task an action handler may touch.
//
// Deliberately narrow: handlers never receive the task record itself, because
// the scheduler owns that state and hands out copies.
type Handle interface {
	// ID is the task's identifier.
	ID() string
	// Params are the submitted parameters.
	Params() map[string]any
	// Progress publishes an incremental update to subscribers.
	Progress(payload map[string]any)
	// Cancelled reports whether cancellation has been requested.
	Cancelled() bool
	// MarkController releases this task's concurrency slot so a coordinator can
	// wait on its children without deadlocking its own service.
	MarkController()
}

// Registration pairs a definition with its handler.
type Registration struct {
	Definition Definition
	Handler    Handler
}

// Registry holds registered actions, keyed by action id.
//
// Several definitions may share an id - typically a detail-view variant and a
// library variant - and Resolve picks between them using the request context.
type Registry struct {
	mu      sync.RWMutex
	entries map[string][]Registration
	// order preserves registration order across ids so ListAll is stable; the
	// frontend renders the action list in the order it receives it.
	order []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string][]Registration)}
}

// Register adds a definition and its handler.
//
// DeduplicateSubmissions defaults to true, matching the Python model's default;
// callers that want duplicates must opt out explicitly.
func (r *Registry) Register(def Definition, handler Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, seen := r.entries[def.ID]; !seen {
		r.order = append(r.order, def.ID)
	}
	r.entries[def.ID] = append(r.entries[def.ID], Registration{Definition: def, Handler: handler})
}

// ListAll returns every registered definition in registration order.
func (r *Registry) ListAll() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []Definition
	for _, id := range r.order {
		for _, e := range r.entries[id] {
			out = append(out, e.Definition)
		}
	}
	return out
}

// Available returns the definitions applicable to a context, in registration
// order. This backs POST /actions/available.
func (r *Registry) Available(ctx ContextInput) []Definition {
	// Never nil: the endpoint must serialise as [] rather than null.
	out := []Definition{}
	for _, def := range r.ListAll() {
		if def.IsApplicable(ctx) {
			out = append(out, def)
		}
	}
	return out
}

// IDs lists the registered action ids.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// Resolve picks the registration to run for an action id in a context.
//
// The selection logic is ported exactly and is easy to get subtly wrong:
//  1. Prefer a definition whose kind matches the view - "detail" when the
//     request is a detail view, "library" otherwise.
//  2. Failing that, fall back to the FIRST applicable definition regardless of
//     kind.
//
// Returns false when the id is unknown or nothing applies.
func (r *Registry) Resolve(id string, ctx ContextInput) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := r.entries[id]
	if len(entries) == 0 {
		return Registration{}, false
	}

	preferred := "library"
	if ctx.IsDetailView {
		preferred = "detail"
	}

	var fallback *Registration
	for i := range entries {
		e := entries[i]
		if !e.Definition.IsApplicable(ctx) {
			continue
		}
		if e.Definition.kind() == preferred {
			return e, true
		}
		if fallback == nil {
			fallback = &entries[i]
		}
	}

	if fallback != nil {
		return *fallback, true
	}
	return Registration{}, false
}

// Get returns every registration for an id, applicable or not.
func (r *Registry) Get(id string) []Registration {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]Registration(nil), r.entries[id]...)
}

// UnregisterService drops every action belonging to a service. Used when a
// plugin is unloaded or its host restarts.
func (r *Registry) UnregisterService(service string) {
	if service == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var keptOrder []string
	for _, id := range r.order {
		var kept []Registration
		for _, e := range r.entries[id] {
			if e.Definition.Service != service {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(r.entries, id)
			continue
		}
		r.entries[id] = kept
		keptOrder = append(keptOrder, id)
	}
	r.order = keptOrder
}
