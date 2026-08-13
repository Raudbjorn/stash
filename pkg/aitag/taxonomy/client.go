package taxonomy

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/scraper"
)

// Client provides freshness, category selection, and label resolution over a
// taxonomy cache.
type Client struct {
	Cache              *Cache
	Endpoint           string
	APIKey             string
	StaleTolerance     time.Duration
	ExcludeTagPatterns []string
}

// CandidatesByCategory returns the requested categories, deduplicated by
// StashDB ID. Category names are matched case-insensitively.
func (c *Client) CandidatesByCategory(ctx context.Context, categories []string) (map[string][]Entry, error) {
	if err := c.ensure(ctx); err != nil {
		return nil, err
	}
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
