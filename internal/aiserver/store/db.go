// Package store owns the AI server's database: a file separate from Stash's own
// SQLite database, so AI schema churn can never block or corrupt Stash's
// migration chain.
//
// The server deliberately reuses Stash's mature sqlite3 driver rather than
// introducing a second SQLite implementation into the process.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const driverName = "sqlite3"

type sqlRunner interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// DB is a handle to the AI database.
type DB struct {
	sql  sqlRunner
	conn *sql.DB
	tx   *sql.Tx
	path string
}

func databaseDSN(path string) string {
	dsn := (&url.URL{Scheme: "file", Path: path}).String()
	return dsn + "?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_foreign_keys=on&_txlock=immediate"
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

	conn, err := sql.Open(driverName, databaseDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// WAL permits readers while the single SQLite writer is active. A bounded
	// pool prevents long plugin queries from monopolising health checks and
	// scheduler reads; SQLite itself serialises writes.
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(4)

	if err := conn.PingContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}

	db := &DB{sql: conn, conn: conn, path: path}
	if err := db.migrate(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return db, nil
}

// SQL exposes the underlying handle for the sibling packages that build queries.
func (db *DB) SQL() *sql.DB { return db.conn }

// Path is the on-disk location, for diagnostics and the health endpoint.
func (db *DB) Path() string { return db.path }

// Close releases the connection.
func (db *DB) Close() error {
	if db == nil || db.conn == nil || db.tx != nil {
		return nil
	}
	return db.conn.Close()
}

// Ping reports whether the database is reachable.
func (db *DB) Ping(ctx context.Context) error {
	if db == nil || db.conn == nil {
		return errors.New("aiserver/store: database not open")
	}
	return db.conn.PingContext(ctx)
}

// WithTx returns a lightweight view whose reads and writes use tx. Calling
// InTx on the view reuses that transaction rather than opening a savepoint.
func (db *DB) WithTx(tx *sql.Tx) *DB {
	return &DB{sql: tx, conn: db.conn, tx: tx, path: db.path}
}

// InTx runs fn inside a transaction, rolling back on error or panic.
//
// Note there are deliberately no nested transactions or savepoints in this
// package. Ingest validates rows before opening its transaction, avoiding
// per-row transaction overhead and partial batches.
func (db *DB) InTx(ctx context.Context, fn func(*sql.Tx) error) (err error) {
	if db.tx != nil {
		return fn(db.tx)
	}
	tx, err := db.conn.BeginTx(ctx, nil)
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
