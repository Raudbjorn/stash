// Package store owns the AI server's database: a file separate from Stash's own
// SQLite database, so AI schema churn can never block or corrupt Stash's
// migration chain.
//
// Everything here is written against database/sql only. The driver is a
// deliberate seam: Turso is pre-1.0, and swapping to mattn/go-sqlite3 must stay
// a one-line change in Open.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "turso.tech/database/tursogo"
)

// driverName is the seam. Phase 0 measured this engine at roughly 6.5k
// inserts/sec versus 114k for mattn/go-sqlite3; that is comfortable for the
// ingest batches this server handles, but if it ever stops being so, changing
// this constant and the DSN in Open is the whole migration.
const driverName = "turso"

// DB is a handle to the AI database.
type DB struct {
	sql  *sql.DB
	path string
}

// Open opens (creating if necessary) the AI database at path and brings its
// schema up to date. The parent directory is created if missing.
func Open(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("aiserver/store: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	conn, err := sql.Open(driverName, path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// Single connection. The engine is single-writer and pre-1.0; correctness
	// beats read parallelism until the ingest path has been load-tested. The
	// ingest handler writes a whole batch per request, so it is serialised here
	// anyway.
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}

	db := &DB{sql: conn, path: path}
	if err := db.migrate(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return db, nil
}

// SQL exposes the underlying handle for the sibling packages that build queries.
func (db *DB) SQL() *sql.DB { return db.sql }

// Path is the on-disk location, for diagnostics and the health endpoint.
func (db *DB) Path() string { return db.path }

// Close releases the connection.
func (db *DB) Close() error {
	if db == nil || db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

// Ping reports whether the database is reachable.
func (db *DB) Ping(ctx context.Context) error {
	if db == nil || db.sql == nil {
		return errors.New("aiserver/store: database not open")
	}
	return db.sql.PingContext(ctx)
}

// InTx runs fn inside a transaction, rolling back on error or panic.
//
// Note there are deliberately no nested transactions or savepoints anywhere in
// this package: the engine reports SQLITE_BUSY for SAVEPOINT while a write is in
// flight, so the ingest path validates rows before opening its transaction
// rather than wrapping each row in one.
func (db *DB) InTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p)
		}
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// maxSQLParams bounds how many placeholders a single statement may carry.
// SQLite's documented default is 999 and this engine's limit is unspecified, so
// every `IN (...)` in this package chunks through ChunkInts at this width.
// Phase 0 verified 600 parameters work, making 500 comfortably safe.
const maxSQLParams = 500

// ChunkInts splits ids into batches small enough for a single `IN (...)`.
// Returns a single empty batch for empty input so callers can range naturally
// without special-casing.
func ChunkInts(ids []int, size int) [][]int {
	if size <= 0 {
		size = maxSQLParams
	}
	if len(ids) == 0 {
		return nil
	}
	var out [][]int
	for start := 0; start < len(ids); start += size {
		end := min(start+size, len(ids))
		out = append(out, ids[start:end])
	}
	return out
}

// ChunkStrings is ChunkInts for string keys, used by the interaction
// pre-deduplication query over client_event_id.
func ChunkStrings(keys []string, size int) [][]string {
	if size <= 0 {
		size = maxSQLParams
	}
	if len(keys) == 0 {
		return nil
	}
	var out [][]string
	for start := 0; start < len(keys); start += size {
		end := min(start+size, len(keys))
		out = append(out, keys[start:end])
	}
	return out
}

// Placeholders renders n comma-separated `?` markers for an IN clause.
func Placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	buf := make([]byte, 0, n*2-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '?')
	}
	return string(buf)
}
