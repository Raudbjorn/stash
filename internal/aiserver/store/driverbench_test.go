package store

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	_ "turso.tech/database/tursogo"
)

// The interactions ingest path writes a whole event batch per HTTP request, so
// bulk-insert throughput is the number that decides whether Turso is viable
// here. This measures it against mattn/go-sqlite3, the documented fallback.
//
// Run with: go test ./internal/aiserver/store/ -run TestDriverInsertThroughput -v
func TestDriverInsertThroughput(t *testing.T) {
	const rows = 10000

	cases := []struct {
		driver string
		dsn    func(dir string) string
	}{
		{"turso", func(dir string) string {
			return filepath.Join(dir, "bench.db")
		}},
		{"sqlite3", func(dir string) string {
			return "file:" + filepath.Join(dir, "bench.db") +
				"?_journal=WAL&_sync=NORMAL&_busy_timeout=50&_txlock=immediate"
		}},
	}

	for _, tc := range cases {
		t.Run(tc.driver, func(t *testing.T) {
			db, err := sql.Open(tc.driver, tc.dsn(t.TempDir()))
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer db.Close()
			db.SetMaxOpenConns(1)

			if _, err := db.Exec(`CREATE TABLE t (
				id INTEGER PRIMARY KEY,
				session_id TEXT NOT NULL,
				scene_id INTEGER NOT NULL,
				start_s REAL NOT NULL,
				end_s REAL NOT NULL,
				created_at INTEGER NOT NULL)`); err != nil {
				t.Fatalf("create: %v", err)
			}

			start := time.Now()
			tx, err := db.Begin()
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			stmt, err := tx.Prepare(`INSERT INTO t (session_id, scene_id, start_s, end_s, created_at) VALUES (?,?,?,?,?)`)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			for i := 0; i < rows; i++ {
				if _, err := stmt.Exec(fmt.Sprintf("sess-%d", i%50), i, float64(i), float64(i+2), int64(i)); err != nil {
					t.Fatalf("exec: %v", err)
				}
			}
			stmt.Close()
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
			elapsed := time.Since(start)

			t.Logf("%-8s %d rows in %v (%.0f rows/sec)",
				tc.driver, rows, elapsed.Round(time.Millisecond),
				float64(rows)/elapsed.Seconds())
		})
	}
}
