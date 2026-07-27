package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Plugin metadata, sources and catalog.
//
// All three live in Go rather than in the host process, which is what lets most
// of the plugin API keep answering while the host is dead or restarting: what
// is installed, where it came from and what is available are facts about disk
// and the database, not about a running interpreter.

// Plugin statuses. These reach the UI verbatim.
const (
	PluginStatusActive   = "active"
	PluginStatusError    = "error"
	PluginStatusDisabled = "disabled"
	PluginStatusRemoved  = "removed"
)

// LocalSourceName is the built-in source for plugins installed by hand.
//
// Immutable: it has no index to refresh and deleting it would orphan the
// plugins already installed under it.
const LocalSourceName = "local"

// ErrNotFound is returned when a named row does not exist.
var ErrNotFound = errors.New("NOT_FOUND")

// ErrSourceImmutable is returned for operations on the local source.
var ErrSourceImmutable = errors.New("SOURCE_IMMUTABLE")

// PluginMeta is an installed plugin's record.
//
// The JSON names match what the shipped settings UI reads, so they are a
// contract rather than a preference.
type PluginMeta struct {
	Name            string  `json:"name"`
	Version         string  `json:"version"`
	Status          string  `json:"status"`
	RequiredBackend string  `json:"required_backend"`
	MigrationHead   *string `json:"migration_head"`
	LastError       *string `json:"last_error"`
	HumanName       *string `json:"human_name"`
	ServerLink      *string `json:"server_link"`
}

// PluginSource is a catalog source.
type PluginSource struct {
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	URL             string  `json:"url"`
	Enabled         bool    `json:"enabled"`
	LastRefreshedAt *int64  `json:"last_refreshed_at"`
	LastError       *string `json:"last_error"`
}

// CatalogEntry is one plugin advertised by a source.
type CatalogEntry struct {
	PluginName   string         `json:"plugin_name"`
	Version      string         `json:"version"`
	Description  *string        `json:"description"`
	HumanName    *string        `json:"human_name"`
	ServerLink   *string        `json:"server_link"`
	Manifest     map[string]any `json:"manifest"`
	Dependencies []string       `json:"-"`
	SourceID     int64          `json:"-"`
}

// ----------------------------------------------------------------- meta ----

// ListPluginMeta returns installed plugins in name order.
//
// activeOnly and includeRemoved reproduce the query parameters the shipped UI
// sends; removed rows are hidden by default because they are tombstones kept
// for their settings, not things a user can act on.
func (db *DB) ListPluginMeta(ctx context.Context, activeOnly, includeRemoved bool) ([]PluginMeta, error) {
	query := `SELECT name, version, status, required_backend, migration_head,
	                 last_error, human_name, server_link
	          FROM plugin_meta`

	var conditions []string
	if activeOnly {
		conditions = append(conditions, "status = 'active'")
	} else if !includeRemoved {
		conditions = append(conditions, "status != 'removed'")
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY name"

	rows, err := db.sql.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PluginMeta{}
	for rows.Next() {
		var meta PluginMeta
		if err := rows.Scan(&meta.Name, &meta.Version, &meta.Status, &meta.RequiredBackend,
			&meta.MigrationHead, &meta.LastError, &meta.HumanName, &meta.ServerLink); err != nil {
			return nil, err
		}
		dropNullStrings(&meta.MigrationHead, &meta.LastError, &meta.HumanName, &meta.ServerLink)
		out = append(out, meta)
	}
	return out, rows.Err()
}

// GetPluginMeta returns one plugin's record.
func (db *DB) GetPluginMeta(ctx context.Context, name string) (PluginMeta, error) {
	var meta PluginMeta
	err := db.sql.QueryRowContext(ctx,
		`SELECT name, version, status, required_backend, migration_head,
		        last_error, human_name, server_link
		 FROM plugin_meta WHERE name = ?`, name).
		Scan(&meta.Name, &meta.Version, &meta.Status, &meta.RequiredBackend,
			&meta.MigrationHead, &meta.LastError, &meta.HumanName, &meta.ServerLink)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginMeta{}, ErrNotFound
	}
	if err != nil {
		return PluginMeta{}, err
	}
	dropNullStrings(&meta.MigrationHead, &meta.LastError, &meta.HumanName, &meta.ServerLink)
	return meta, nil
}

// UpsertPluginMeta records an installed plugin.
func (db *DB) UpsertPluginMeta(ctx context.Context, meta PluginMeta) error {
	now := NowMillis()
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO plugin_meta
		     (name, version, required_backend, status, migration_head, last_error,
		      human_name, server_link, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		     version          = excluded.version,
		     required_backend = excluded.required_backend,
		     status           = excluded.status,
		     migration_head   = excluded.migration_head,
		     last_error       = excluded.last_error,
		     human_name       = excluded.human_name,
		     server_link      = excluded.server_link,
		     updated_at       = excluded.updated_at`,
		meta.Name, meta.Version, meta.RequiredBackend, meta.Status, meta.MigrationHead,
		meta.LastError, meta.HumanName, meta.ServerLink, now, now)
	return err
}

// SetPluginStatus records a plugin's status and, when it failed, why.
func (db *DB) SetPluginStatus(ctx context.Context, name, status, lastError string) error {
	var errText *string
	if lastError != "" {
		// Bounded: a Python traceback is long and the UI shows one line of it.
		errText = ptr(truncate(lastError, 2000))
	}

	result, err := db.sql.ExecContext(ctx,
		`UPDATE plugin_meta SET status = ?, last_error = ?, updated_at = ? WHERE name = ?`,
		status, errText, NowMillis(), name)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePluginMeta removes a plugin's record.
func (db *DB) DeletePluginMeta(ctx context.Context, name string) error {
	_, err := db.sql.ExecContext(ctx, `DELETE FROM plugin_meta WHERE name = ?`, name)
	return err
}

// --------------------------------------------------------------- sources ---

// SeedLocalSource ensures the built-in source exists.
func (db *DB) SeedLocalSource(ctx context.Context) error {
	now := NowMillis()
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO plugin_sources (name, url, enabled, created_at, updated_at)
		 VALUES (?, '', 1, ?, ?)
		 ON CONFLICT(name) DO NOTHING`,
		LocalSourceName, now, now)
	return err
}

// ListPluginSources returns sources in name order.
func (db *DB) ListPluginSources(ctx context.Context) ([]PluginSource, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, name, url, enabled, last_refreshed_at, last_error
		 FROM plugin_sources ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PluginSource{}
	for rows.Next() {
		var src PluginSource
		var enabled int
		if err := rows.Scan(&src.ID, &src.Name, &src.URL, &enabled,
			&src.LastRefreshedAt, &src.LastError); err != nil {
			return nil, err
		}
		src.Enabled = enabled != 0
		dropNullStrings(&src.LastError)
		out = append(out, src)
	}
	return out, rows.Err()
}

// GetPluginSource returns one source by name.
func (db *DB) GetPluginSource(ctx context.Context, name string) (PluginSource, error) {
	var src PluginSource
	var enabled int
	err := db.sql.QueryRowContext(ctx,
		`SELECT id, name, url, enabled, last_refreshed_at, last_error
		 FROM plugin_sources WHERE name = ?`, name).
		Scan(&src.ID, &src.Name, &src.URL, &enabled, &src.LastRefreshedAt, &src.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginSource{}, ErrNotFound
	}
	if err != nil {
		return PluginSource{}, err
	}
	src.Enabled = enabled != 0
	dropNullStrings(&src.LastError)
	return src, nil
}

// UpsertPluginSource adds or updates a source and returns it.
func (db *DB) UpsertPluginSource(ctx context.Context, name, url string, enabled bool) (PluginSource, error) {
	if name == LocalSourceName {
		return PluginSource{}, ErrSourceImmutable
	}

	now := NowMillis()
	enabledInt := 0
	if enabled {
		enabledInt = 1
	}

	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO plugin_sources (name, url, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET
		     url        = excluded.url,
		     enabled    = excluded.enabled,
		     updated_at = excluded.updated_at`,
		name, url, enabledInt, now, now)
	if err != nil {
		return PluginSource{}, err
	}
	return db.GetPluginSource(ctx, name)
}

// DeletePluginSource removes a source and its catalog entries.
func (db *DB) DeletePluginSource(ctx context.Context, name string) error {
	if name == LocalSourceName {
		return ErrSourceImmutable
	}

	src, err := db.GetPluginSource(ctx, name)
	if err != nil {
		return err
	}

	// Children go explicitly: the declared cascade is documentation, and this
	// engine was measured not to enforce it.
	return db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM plugin_catalog WHERE source_id = ?`, src.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM plugin_sources WHERE id = ?`, src.ID)
		return err
	})
}

// MarkSourceRefreshed records the outcome of a catalog refresh.
func (db *DB) MarkSourceRefreshed(ctx context.Context, sourceID int64, refreshErr string) error {
	var errText *string
	if refreshErr != "" {
		errText = ptr(truncate(refreshErr, 500))
	}
	_, err := db.sql.ExecContext(ctx,
		`UPDATE plugin_sources SET last_refreshed_at = ?, last_error = ?, updated_at = ? WHERE id = ?`,
		NowMillis(), errText, NowMillis(), sourceID)
	return err
}

// --------------------------------------------------------------- catalog ---

// ReplaceCatalog swaps a source's catalog for a freshly fetched one.
//
// Replace rather than diff: the index is the source of truth, an entry that
// disappeared from it should disappear here, and the volumes are small enough
// that a diff would only add ways to be wrong.
func (db *DB) ReplaceCatalog(ctx context.Context, sourceID int64, entries []CatalogEntry) error {
	now := NowMillis()

	return db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM plugin_catalog WHERE source_id = ?`, sourceID); err != nil {
			return err
		}

		for _, entry := range entries {
			var dependencies any
			if len(entry.Dependencies) > 0 {
				encoded, err := json.Marshal(map[string]any{"plugins": entry.Dependencies})
				if err != nil {
					return err
				}
				dependencies = string(encoded)
			}

			var manifest any
			if entry.Manifest != nil {
				encoded, err := json.Marshal(entry.Manifest)
				if err != nil {
					return err
				}
				manifest = string(encoded)
			}

			if _, err := tx.ExecContext(ctx,
				`INSERT INTO plugin_catalog
				     (source_id, plugin_name, version, description, human_name,
				      server_link, dependencies_json, manifest_json, updated_at)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sourceID, entry.PluginName, entry.Version, entry.Description,
				entry.HumanName, entry.ServerLink, dependencies, manifest, now); err != nil {
				return fmt.Errorf("store catalog entry %s: %w", entry.PluginName, err)
			}
		}
		return nil
	})
}

// ListCatalog returns a source's catalog entries in plugin-name order.
func (db *DB) ListCatalog(ctx context.Context, sourceID int64) ([]CatalogEntry, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT plugin_name, version, description, human_name, server_link,
		        dependencies_json, manifest_json, source_id
		 FROM plugin_catalog WHERE source_id = ? ORDER BY plugin_name`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCatalog(rows)
}

// FindCatalogEntries returns every source's entry for a plugin.
//
// A plugin may be advertised by several sources; the caller decides which wins,
// since a preferred source is part of an install request.
func (db *DB) FindCatalogEntries(ctx context.Context, pluginName string) ([]CatalogEntry, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT plugin_name, version, description, human_name, server_link,
		        dependencies_json, manifest_json, source_id
		 FROM plugin_catalog WHERE plugin_name = ? ORDER BY source_id`, pluginName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCatalog(rows)
}

func scanCatalog(rows *sql.Rows) ([]CatalogEntry, error) {
	out := []CatalogEntry{}
	for rows.Next() {
		var (
			entry        CatalogEntry
			dependencies JSONText[map[string][]string]
			manifest     JSONText[map[string]any]
		)
		if err := rows.Scan(&entry.PluginName, &entry.Version, &entry.Description,
			&entry.HumanName, &entry.ServerLink, &dependencies, &manifest, &entry.SourceID); err != nil {
			return nil, err
		}

		dropNullStrings(&entry.Description, &entry.HumanName, &entry.ServerLink)
		if dependencies.Valid {
			entry.Dependencies = dependencies.Data["plugins"]
		}
		if manifest.Valid {
			entry.Manifest = manifest.Data
		}
		out = append(out, entry)
	}
	return out, rows.Err()
}

// dropNullStrings nils out pointers holding a placeholder null token.
//
// Older catalog entries and hand-written manifests contain the literal string
// "null" where a value is absent; storing it would make it compare as a real
// value and render as "null" in the UI.
func dropNullStrings(values ...**string) {
	for _, target := range values {
		if *target == nil {
			continue
		}
		if NormalizeNullStrings(**target) == nil {
			*target = nil
		}
	}
}

func ptr[T any](v T) *T { return &v }

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}
