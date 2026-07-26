package markersync

import "context"

// TagResolver turns a tag name into a tag ID, creating the tag if it does not
// already exist ("get or create"). The concrete implementation backed by the
// tag repository (FindByName -> FindByAlias -> Create) lives in the manager
// layer (Stage 2); this package only depends on the interface.
type TagResolver interface {
	ResolveOrCreate(ctx context.Context, name string) (int, error)
}

// CachingTagResolver wraps an inner TagResolver with a per-run in-memory cache
// so that repeated lookups of the same tag name within a single sync run avoid
// redundant work against the underlying store. It is not safe for concurrent
// use; construct one per run.
type CachingTagResolver struct {
	inner TagResolver
	cache map[string]int
}

// NewCachingTagResolver returns a CachingTagResolver wrapping inner.
func NewCachingTagResolver(inner TagResolver) *CachingTagResolver {
	return &CachingTagResolver{
		inner: inner,
		cache: make(map[string]int),
	}
}

// ResolveOrCreate implements TagResolver, caching successful resolutions.
func (c *CachingTagResolver) ResolveOrCreate(ctx context.Context, name string) (int, error) {
	if id, ok := c.cache[name]; ok {
		return id, nil
	}

	id, err := c.inner.ResolveOrCreate(ctx, name)
	if err != nil {
		return 0, err
	}

	c.cache[name] = id
	return id, nil
}

var _ TagResolver = (*CachingTagResolver)(nil)
