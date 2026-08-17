package taxonomy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/scraper"
)

// Client provides freshness, category selection, and label resolution over a
// taxonomy cache.
type Client struct {
	mu                 sync.Mutex
	Cache              *Cache
	bm25               *BM25
	bm25BuiltAt        time.Time
	bm25Entries        int
	bm25Aliases        int
	Endpoint           string
	APIKey             string
	StaleTolerance     time.Duration
	ExcludeTagPatterns []string
}

// CandidatesByCategory returns the requested categories, deduplicated by
// StashDB ID. Category names are matched case-insensitively.
func (c *Client) CandidatesByCategory(ctx context.Context, categories []string) (map[string][]Entry, error) {
	if c == nil {
		return nil, fmt.Errorf("taxonomy client is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.rebuildBM25Locked()
	wanted := make(map[string]string, len(categories))
	for _, category := range categories {
		category = strings.TrimSpace(category)
		if category != "" {
			wanted[strings.ToLower(category)] = category
		}
	}
	out := make(map[string][]Entry, len(wanted))
	seen := make(map[string]struct{}, len(c.Cache.Entries))
	excluded := scraper.CompileExclusionRegexps(c.ExcludeTagPatterns)
	entries := sortedEntries(c.Cache.Entries)
	for _, entry := range entries {
		category, ok := wanted[strings.ToLower(strings.TrimSpace(entry.Category))]
		if !ok || matchesAny(excluded, entry.Canonical) {
			continue
		}
		if _, ok := seen[entry.StashID]; ok {
			continue
		}
		seen[entry.StashID] = struct{}{}
		out[category] = append(out[category], entry)
	}
	for _, category := range wanted {
		if _, ok := out[category]; !ok {
			out[category] = []Entry{}
		}
	}
	return out, nil
}

// Resolve maps a canonical name or alias to its authoritative taxonomy entry.
func (c *Client) Resolve(ctx context.Context, label string) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensure(ctx); err != nil {
		return Entry{}, false
	}
	label = normalizeLabel(label)
	compact := compactLabel(label)
	if label == "" {
		return Entry{}, false
	}
	for _, entry := range sortedEntries(c.Cache.Entries) {
		canonical := normalizeLabel(entry.Canonical)
		if canonical == label || compactLabel(canonical) == compact {
			return entry, true
		}
		for _, alias := range entry.Aliases {
			normalized := normalizeLabel(alias)
			if normalized == label || compactLabel(normalized) == compact {
				return entry, true
			}
		}
	}
	return Entry{}, false
}

// BM25Index returns the lexical index for the active cache. The returned index
// is safe for concurrent reads and remains valid across atomic rebuilds.
func (c *Client) BM25Index(ctx context.Context) (*BM25, error) {
	if c == nil {
		return nil, fmt.Errorf("taxonomy client is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
	c.rebuildBM25Locked()
	return c.bm25, nil
}

// BM25Top returns lexical taxonomy candidates ranked by the active cache.
func (c *Client) BM25Top(ctx context.Context, query string, k int) ([]BM25Hit, error) {
	index, err := c.BM25Index(ctx)
	if err != nil {
		return nil, err
	}
	return index.Top(query, k), nil
}

// Refresh forces an authoritative fetch and atomically replaces the active
// cache after the complete taxonomy has been persisted.
func (c *Client) Refresh(ctx context.Context, endpoint, apiKey string) (Cache, error) {
	if c == nil {
		return Cache{}, fmt.Errorf("taxonomy client is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Cache == nil {
		return Cache{}, fmt.Errorf("taxonomy cache is required")
	}
	next := &Cache{Path: c.Cache.Path}
	if err := next.Fetch(ctx, endpoint, apiKey); err != nil {
		return Cache{}, err
	}
	c.Cache = next
	c.Endpoint = endpoint
	c.APIKey = apiKey
	return cloneCache(next), nil
}

// Status returns an immutable snapshot suitable for diagnostics.
func (c *Client) Status() Cache {
	if c == nil {
		return Cache{Entries: map[string]Entry{}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.Cache == nil {
		return Cache{Endpoint: c.Endpoint, Entries: map[string]Entry{}}
	}
	return cloneCache(c.Cache)
}

func cloneCache(cache *Cache) Cache {
	out := *cache
	out.Entries = make(map[string]Entry, len(cache.Entries))
	for id, entry := range cache.Entries {
		entry.Aliases = append([]string(nil), entry.Aliases...)
		out.Entries[id] = entry
	}
	return out
}

func (c *Client) ensure(ctx context.Context) error {
	if c == nil || c.Cache == nil {
		return fmt.Errorf("taxonomy cache is required")
	}
	if c.Cache.Path != "" && len(c.Cache.Entries) == 0 {
		if err := c.Cache.Load(c.Cache.Path); err != nil {
			return err
		}
	}
	endpoint := strings.TrimSpace(c.Endpoint)
	if endpoint == "" {
		if len(c.Cache.Entries) == 0 {
			return fmt.Errorf("taxonomy endpoint is required for an empty cache")
		}
		return nil
	}
	now := time.Now()
	needsRefresh := len(c.Cache.Entries) == 0 || c.Cache.Endpoint != endpoint || !c.Cache.Fresh(now)
	if !needsRefresh {
		return nil
	}
	if err := c.Cache.Fetch(ctx, endpoint, c.APIKey); err != nil {
		if c.staleUsable(now) {
			return nil
		}
		return err
	}
	return nil
}

func (c *Client) rebuildBM25Locked() {
	aliasCount := 0
	for _, entry := range c.Cache.Entries {
		aliasCount += len(entry.Aliases)
	}
	if c.bm25 != nil &&
		c.bm25Entries == len(c.Cache.Entries) &&
		c.bm25Aliases == aliasCount &&
		c.bm25BuiltAt.Equal(c.Cache.UpdatedAt) {
		return
	}
	if c.bm25 == nil {
		c.bm25 = &BM25{}
	}
	c.bm25.Rebuild(sortedEntries(c.Cache.Entries))
	c.bm25Entries = len(c.Cache.Entries)
	c.bm25Aliases = aliasCount
	c.bm25BuiltAt = c.Cache.UpdatedAt
}

func (c *Client) staleUsable(now time.Time) bool {
	if len(c.Cache.Entries) == 0 || c.Cache.UpdatedAt.IsZero() || c.StaleTolerance <= 0 {
		return false
	}
	return now.Sub(c.Cache.UpdatedAt) <= MaxAge+c.StaleTolerance
}

func sortedEntries(entries map[string]Entry) []Entry {
	out := make([]Entry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Canonical == out[j].Canonical {
			return out[i].StashID < out[j].StashID
		}
		return out[i].Canonical < out[j].Canonical
	})
	return out
}

func matchesAny(patterns []*regexp.Regexp, value string) bool {
	value = strings.ToLower(value)
	for _, pattern := range patterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	return false
}

func normalizeLabel(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func compactLabel(value string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(value)
}
