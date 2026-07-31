package recommend

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// Handler runs a recommender.
type Handler func(ctx context.Context, req Request) (Result, error)

// Registration pairs a definition with its handler.
type Registration struct {
	Definition Definition
	Handler    Handler
	// Owner is the plugin that registered it, so a plugin unload can withdraw
	// its recommenders.
	Owner string
}

// Registry holds registered recommenders.
type Registry struct {
	mu      sync.RWMutex
	entries map[string]Registration
	order   []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{entries: make(map[string]Registration)}
}

// Register adds a recommender. Unlike actions, ids are unique: a duplicate is
// a programming error rather than a variant.
func (r *Registry) Register(def Definition, handler Handler, owner string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.entries[def.ID]; exists {
		return fmt.Errorf("recommender already registered: %s", def.ID)
	}
	r.entries[def.ID] = Registration{Definition: def, Handler: handler, Owner: owner}
	r.order = append(r.order, def.ID)
	return nil
}

// Get returns a registration by id.
func (r *Registry) Get(id string) (Registration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	reg, ok := r.entries[id]
	return reg, ok
}

// ListForContext returns the recommenders offered in a context, in
// registration order.
func (r *Registry) ListForContext(ctx Context) []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Never nil: the endpoint must serialise as [].
	out := []Definition{}
	for _, id := range r.order {
		reg, ok := r.entries[id]
		if ok && reg.Definition.AppliesTo(ctx) {
			out = append(out, reg.Definition)
		}
	}
	return out
}

// ListAll returns every registered definition.
func (r *Registry) ListAll() []Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := []Definition{}
	for _, id := range r.order {
		if reg, ok := r.entries[id]; ok {
			out = append(out, reg.Definition)
		}
	}
	return out
}

// UnregisterOwner withdraws every recommender belonging to a plugin.
func (r *Registry) UnregisterOwner(owner string) {
	if owner == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	var kept []string
	for _, id := range r.order {
		reg, ok := r.entries[id]
		if !ok {
			continue
		}
		if reg.Owner == owner {
			delete(r.entries, id)
			continue
		}
		kept = append(kept, id)
	}
	r.order = kept
}

// IDs lists registered recommender ids, sorted.
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]string, 0, len(r.entries))
	for id := range r.entries {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
