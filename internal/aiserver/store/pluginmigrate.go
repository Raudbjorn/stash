package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Plugin migrations.
//
// The Python server let plugins ship .py migrations, which meant arbitrary code
// ran against the whole database before the plugin had even loaded. These are
// .sql instead, applied by Go, recorded with a checksum, and restricted to the
// plugin's own p_<name>_ namespace.
//
// The audit that justified the change found exactly one plugin shipping .py
// migrations, and it was a demo - so the compatibility cost is nil and the
// isolation gain is real.

// migrationPattern matches NNNN_description.sql, which fixes the order.
var migrationPattern = regexp.MustCompile(`^(\d{4})_[A-Za-z0-9._-]+\.sql$`)

// PluginMigration is one migration file.
type PluginMigration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// ensurePluginMigrationLedger creates the table recording what has been applied.
func (db *DB) ensurePluginMigrationLedger(ctx context.Context) error {
	_, err := db.sql.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS plugin_migrations (
		    plugin     TEXT NOT NULL,
		    version    INTEGER NOT NULL,
		    name       TEXT NOT NULL,
		    checksum   TEXT NOT NULL,
		    applied_at INTEGER NOT NULL,
		    PRIMARY KEY (plugin, version)
		) STRICT`)
	return err
}

// LoadPluginMigrations reads a plugin's migrations directory.
//
// Files that do not match the naming convention are ignored rather than
// rejected: a README beside the migrations is not an error.
func LoadPluginMigrations(dir string) ([]PluginMigration, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var out []PluginMigration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationPattern.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}

		var version int
		if _, err := fmt.Sscanf(match[1], "%d", &version); err != nil {
			continue
		}

		sum := sha256.Sum256(data)
		out = append(out, PluginMigration{
			Version:  version,
			Name:     entry.Name(),
			SQL:      string(data),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// ApplyPluginMigrations brings a plugin's schema up to date.
//
// Returns the version now applied, which becomes the plugin's migration_head.
func (db *DB) ApplyPluginMigrations(ctx context.Context, plugin string, migrations []PluginMigration) (int, error) {
	if err := db.ensurePluginMigrationLedger(ctx); err != nil {
		return 0, err
	}

	applied, err := db.appliedPluginMigrations(ctx, plugin)
	if err != nil {
		return 0, err
	}

	head := 0
	for version := range applied {
		if version > head {
			head = version
		}
	}

	for _, migration := range migrations {
		if existing, ok := applied[migration.Version]; ok {
			// An edited migration is a bug worth reporting, not something to
			// silently re-run: the database already has the old shape.
			if existing != migration.Checksum {
				return head, fmt.Errorf(
					"plugin %s migration %s has changed since it was applied; bump its version instead",
					plugin, migration.Name)
			}
			continue
		}

		if err := validatePluginMigration(plugin, migration); err != nil {
			return head, fmt.Errorf("plugin %s migration %s: %w", plugin, migration.Name, err)
		}

		err := db.InTx(ctx, func(tx *sql.Tx) error {
			for _, statement := range splitStatements(migration.SQL) {
				if _, err := tx.ExecContext(ctx, statement); err != nil {
					return fmt.Errorf("%q: %w", firstLine(statement), err)
				}
			}
			_, err := tx.ExecContext(ctx,
				`INSERT INTO plugin_migrations (plugin, version, name, checksum, applied_at)
				 VALUES (?, ?, ?, ?, ?)`,
				plugin, migration.Version, migration.Name, migration.Checksum, NowMillis())
			return err
		})
		if err != nil {
			return head, fmt.Errorf("plugin %s migration %s: %w", plugin, migration.Name, err)
		}

		head = migration.Version
	}

	return head, nil
}

// DropPluginTables removes everything a plugin created.
//
// Called when a plugin is removed with its data. The namespace rule is what
// makes this possible to do correctly: a plugin's tables are exactly those
// whose names carry its prefix.
func (db *DB) DropPluginTables(ctx context.Context, plugin string) error {
	// The ledger may not exist: a plugin can be installed and removed without
	// ever shipping a migration.
	if err := db.ensurePluginMigrationLedger(ctx); err != nil {
		return err
	}

	prefix := PluginTablePrefix(plugin)

	rows, err := db.sql.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name LIKE ?`, prefix+"%")
	if err != nil {
		return err
	}

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, table := range tables {
		// The name came from sqlite_master and matched the plugin's own prefix,
		// so it cannot be attacker-chosen; quoted anyway.
		if _, err := db.sql.ExecContext(ctx, `DROP TABLE IF EXISTS "`+table+`"`); err != nil {
			return err
		}
	}

	_, err = db.sql.ExecContext(ctx, `DELETE FROM plugin_migrations WHERE plugin = ?`, plugin)
	return err
}

func (db *DB) appliedPluginMigrations(ctx context.Context, plugin string) (map[int]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT version, checksum FROM plugin_migrations WHERE plugin = ?`, plugin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int]string)
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, err
		}
		out[version] = checksum
	}
	return out, rows.Err()
}

// validatePluginMigration holds a migration to the plugin's own namespace.
//
// Without this a migration could DROP the server's tables, and it runs before
// the plugin has been vetted in any other way.
func validatePluginMigration(plugin string, migration PluginMigration) error {
	prefix := PluginTablePrefix(plugin)

	for _, statement := range splitStatements(migration.SQL) {
		trimmed := strings.TrimSpace(statement)
		if trimmed == "" {
			continue
		}

		fields := strings.Fields(strings.ToLower(trimmed))
		if len(fields) == 0 {
			continue
		}
		if forbiddenVerbs[fields[0]] {
			return fmt.Errorf("%s is not permitted in a plugin migration", strings.ToUpper(fields[0]))
		}

		for _, match := range tableRefPattern.FindAllStringSubmatch(blankStringLiterals(trimmed), -1) {
			table := unquoteIdentifier(match[1])
			if strings.EqualFold(table, "if") || strings.EqualFold(table, "exists") {
				continue
			}
			if !strings.HasPrefix(strings.ToLower(table), prefix) {
				return ErrPluginTable{Plugin: plugin, Table: table}
			}
		}
	}
	return nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	return truncate(s, 120)
}
