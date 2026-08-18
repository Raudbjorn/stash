package taxonomy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/stashbox"
	stashboxgraphql "github.com/stashapp/stash/pkg/stashbox/graphql"
)

const MaxAge = 24 * time.Hour

// Entry is one authoritative StashDB taxonomy tag.
type Entry struct {
	StashID     string   `json:"stash_id"`
	Category    string   `json:"category,omitempty"`
	Description string   `json:"description,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Canonical   string   `json:"canonical"`
}

// Cache is the on-disk snapshot of the StashDB tag taxonomy.
// Entries are keyed by StashDB tag ID.
type Cache struct {
	Path      string           `json:"-"`
	Endpoint  string           `json:"endpoint"`
	UpdatedAt time.Time        `json:"updated_at"`
	Entries   map[string]Entry `json:"entries"`
}

// Load replaces the cache from path. A stale cache remains valid until an
// explicit or lazy refresh succeeds. A missing file is an empty cache.
func (c *Cache) Load(path string) error {
	c.Path = path
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if c.Entries == nil {
			c.Entries = make(map[string]Entry)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read taxonomy cache: %w", err)
	}
	var loaded Cache
	if err := json.Unmarshal(data, &loaded); err != nil {
		return fmt.Errorf("decode taxonomy cache: %w", err)
	}
	if loaded.Entries == nil {
		loaded.Entries = make(map[string]Entry)
	}
	loaded.Path = path
	*c = loaded
	return nil
}

// Save atomically persists the cache at path.
func (c *Cache) Save(path string) error {
	if strings.TrimSpace(path) == "" {
		path = c.Path
	}
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("taxonomy cache path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create taxonomy cache directory: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode taxonomy cache: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".taxonomy-cache-*")
	if err != nil {
		return fmt.Errorf("create taxonomy cache temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("set taxonomy cache permissions: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write taxonomy cache: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close taxonomy cache: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace taxonomy cache: %w", err)
	}
	c.Path = path
	return nil
}

// Fetch replaces the in-memory cache with all tags from endpoint, then
// persists it when Path is set. The previous cache remains intact on failure.
func (c *Cache) Fetch(ctx context.Context, endpoint, apiKey string) error {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return fmt.Errorf("taxonomy endpoint is required")
	}
	client := stashbox.NewClient(models.StashBox{Endpoint: endpoint, APIKey: apiKey})
	entries := make(map[string]Entry)
	for page := 1; ; page++ {
		result, err := client.QueryTags(ctx, stashboxgraphql.TagQueryInput{
			Page:      page,
			PerPage:   100,
			Direction: stashboxgraphql.SortDirectionEnumAsc,
			Sort:      stashboxgraphql.TagSortEnumName,
		})
		if err != nil {
			return fmt.Errorf("fetch taxonomy page %d: %w", page, err)
		}
		for _, tag := range result.QueryTags.Tags {
			if tag == nil || strings.TrimSpace(tag.ID) == "" || strings.TrimSpace(tag.Name) == "" {
				continue
			}
			entry := Entry{
				StashID:   tag.ID,
				Aliases:   append([]string(nil), tag.Aliases...),
				Canonical: strings.TrimSpace(tag.Name),
			}
			if tag.Description != nil {
				entry.Description = strings.TrimSpace(*tag.Description)
			}
			if tag.Category != nil {
				entry.Category = strings.TrimSpace(tag.Category.Name)
			}
			entries[entry.StashID] = entry
		}
		if len(result.QueryTags.Tags) == 0 || len(entries) >= result.QueryTags.Count {
			break
		}
	}

	updated := Cache{
		Path:      c.Path,
		Endpoint:  endpoint,
		UpdatedAt: time.Now().UTC(),
		Entries:   entries,
	}
	if updated.Path != "" {
		if err := updated.Save(updated.Path); err != nil {
			return err
		}
	}
	*c = updated
	return nil
}

// Fresh reports whether the cache may be used without a lazy refresh.
func (c *Cache) Fresh(now time.Time) bool {
	return len(c.Entries) > 0 && !c.UpdatedAt.IsZero() && now.Sub(c.UpdatedAt) <= MaxAge
}
