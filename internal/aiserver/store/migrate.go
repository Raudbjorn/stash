package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Stash's own database uses golang-migrate bound to mattn/go-sqlite3. That
// driver is not validated against this engine and issues PRAGMA-heavy DDL, so
// the AI database carries its own small migrator instead. It deliberately has no
// down-migrations, matching the Python original whose downgrade() bodies were
// empty in practice.
//
// PRAGMA user_version is avoided too - PRAGMA coverage here is documented as
// partial - in favour of an ordinary table.
const migrationLedgerDDL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT`

type migration struct {
	version int
	name    string
	body    string
}

// migrate applies every pending migration in ascending order, each in its own
// transaction. It is safe to call on every startup.
func (db *DB) migrate(ctx context.Context) error {
	if _, err := db.sql.ExecContext(ctx, migrationLedgerDDL); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}

	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return err
	}

	all, err := loadMigrations()
	if err != nil {
		return err
	}

	for _, m := range all {
		if applied[m.version] {
			continue
		}
		if err := db.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("migration %04d_%s: %w", m.version, m.name, err)
		}
	}
	return nil
}

func (db *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("scan migration ledger: %w", err)
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration ledger: %w", err)
	}
	return applied, nil
}

func (db *DB) applyMigration(ctx context.Context, m migration) error {
	return db.InTx(ctx, func(tx *sql.Tx) error {
		// Statements are executed individually: the driver's Exec handles one
		// statement per call, and doing it this way makes a failure point at
		// the offending statement rather than the whole file.
		for _, stmt := range splitStatements(m.body) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("%w (statement: %.80s)", err, stmt)
			}
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version, name, applied_at) VALUES (?,?,?)`,
			m.version, m.name, NowMillis())
		return err
	})
}

// SchemaVersion reports the highest applied migration, or 0 for a fresh
// database. The version endpoint reports this as db_alembic_head, keeping the
// field name the TypeScript frontend already reads.
func (db *DB) SchemaVersion(ctx context.Context) (int, error) {
	var v sql.NullInt64
	err := db.sql.QueryRowContext(ctx,
		`SELECT MAX(version) FROM schema_migrations`).Scan(&v)
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if !v.Valid {
		return 0, nil
	}
	return int(v.Int64), nil
}

// loadMigrations reads and orders the embedded migrations. Filenames are
// NNNN_description.sql; the numeric prefix is the version and must be unique.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}

	var out []migration
	seen := map[int]string{}

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ".sql")
		prefix, name, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: expected NNNN_description.sql", e.Name())
		}
		version, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version prefix: %w", e.Name(), err)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("migration version %d used by both %q and %q", version, prev, e.Name())
		}
		seen[version] = e.Name()

		body, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: version, name: name, body: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// splitStatements breaks a migration file into individual statements on
// semicolons, discarding comments and blank lines.
//
// This is intentionally simple rather than a real SQL parser: migrations in this
// package are hand-written DDL with no semicolons inside string literals or
// triggers. If that ever stops being true, this needs to grow.
func splitStatements(body string) []string {
	var stmts []string
	var cur strings.Builder

	for line := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		cur.WriteString(line)
		cur.WriteByte('\n')

		if strings.HasSuffix(trimmed, ";") {
			if s := strings.TrimSpace(strings.TrimSuffix(cur.String(), ";\n")); s != "" {
				stmts = append(stmts, s)
			}
			cur.Reset()
		}
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		stmts = append(stmts, s)
	}
	return stmts
}
