package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "turso.tech/database/tursogo"
)

// These tests are the Phase 0 gate for the AI datastore. Every SQL feature the
// ported schema and queries rely on is exercised here against the real engine,
// because Turso is pre-1.0 and its SQLite coverage is documented as partial.
//
// If any of these fail, the store must fall back to mattn/go-sqlite3 - which is
// a one-line change in open(), since everything here is plain database/sql.

func openSpike(t *testing.T) *sql.DB {
	t.Helper()

	path := filepath.Join(t.TempDir(), "spike.db")
	db, err := sql.Open("turso", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Single writer: Turso's engine is single-writer and pre-1.0. Correctness
	// over read parallelism until the ingest path is load-tested.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = db.Close() })

	if err := db.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return db
}

// The shape every AI table uses: STRICT, INTEGER PRIMARY KEY rowid alias,
// millisecond timestamps as INTEGER, JSON payloads as TEXT.
const spikeSchema = `
CREATE TABLE ai_model_runs (
    id          INTEGER PRIMARY KEY,
    service     TEXT    NOT NULL,
    entity_id   INTEGER NOT NULL,
    status      TEXT    NOT NULL DEFAULT 'completed',
    input_params TEXT,
    completed_at INTEGER,
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX ix_runs_service_entity ON ai_model_runs (service, entity_id);
CREATE TABLE ai_result_aggregates (
    id          INTEGER PRIMARY KEY,
    run_id      INTEGER NOT NULL REFERENCES ai_model_runs(id) ON DELETE CASCADE,
    payload_type TEXT   NOT NULL,
    str_value   TEXT,
    metric      TEXT    NOT NULL,
    value_float REAL    NOT NULL
) STRICT;
CREATE UNIQUE INDEX ux_agg ON ai_result_aggregates (run_id, payload_type, str_value, metric);
`

func TestTursoSchemaAndStrictTables(t *testing.T) {
	db := openSpike(t)

	if _, err := db.Exec(spikeSchema); err != nil {
		t.Fatalf("create schema (STRICT tables / indexes / FK): %v", err)
	}

	// STRICT must actually reject a type mismatch, otherwise it is not buying
	// us the safety the plan assumes.
	_, err := db.Exec(`INSERT INTO ai_model_runs (service, entity_id, created_at) VALUES (?,?,?)`,
		"native", "not-an-integer", 1)
	if err == nil {
		t.Error("STRICT table accepted a TEXT value in an INTEGER column; STRICT is not enforced")
	}
}

func TestTursoInsertReturning(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	// store.CreateRun depends on RETURNING rather than LastInsertId.
	var id int64
	err := db.QueryRow(
		`INSERT INTO ai_model_runs (service, entity_id, created_at) VALUES (?,?,?) RETURNING id`,
		"native", 42, 1700000000000,
	).Scan(&id)
	if err != nil {
		t.Fatalf("INSERT...RETURNING: %v", err)
	}
	if id != 1 {
		t.Errorf("RETURNING id = %d, want 1", id)
	}
}

func TestTursoUpsert(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	runID := insertSpikeRun(t, db, "native", 1)

	const upsert = `
INSERT INTO ai_result_aggregates (run_id, payload_type, str_value, metric, value_float)
VALUES (?,?,?,?,?)
ON CONFLICT (run_id, payload_type, str_value, metric)
DO UPDATE SET value_float = excluded.value_float`

	mustExecArgs(t, db, upsert, runID, "tag", "Kissing", "duration_s", 10.5)
	mustExecArgs(t, db, upsert, runID, "tag", "Kissing", "duration_s", 25.0)

	var n int
	var got float64
	if err := db.QueryRow(`SELECT COUNT(*), MAX(value_float) FROM ai_result_aggregates`).Scan(&n, &got); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if n != 1 || got != 25.0 {
		t.Errorf("upsert produced count=%d value=%v, want 1 and 25", n, got)
	}
}

// _store_aggregates groups by label and sums durations; get_scene_tag_totals
// filters with HAVING.
func TestTursoGroupByHaving(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	// Two runs over the same scene, as get_scene_tag_totals sees: durations for
	// a label are summed across runs. The unique index is per-run, so the same
	// label legitimately appears once per run.
	runA := insertSpikeRun(t, db, "native", 1)
	runB := insertSpikeRun(t, db, "native", 1)

	for _, r := range []struct {
		run   int64
		label string
		v     float64
	}{{runA, "Kissing", 10}, {runB, "Kissing", 5}, {runA, "Blowjob", 2}} {
		mustExecArgs(t, db,
			`INSERT INTO ai_result_aggregates (run_id, payload_type, str_value, metric, value_float)
			 VALUES (?,'tag',?, 'duration_s', ?)`, r.run, r.label, r.v)
	}

	rows, err := db.Query(`
SELECT str_value, SUM(value_float) AS total
FROM ai_result_aggregates
WHERE run_id IN (?, ?)
GROUP BY str_value
HAVING SUM(value_float) >= 10
ORDER BY total DESC`, runA, runB)
	if err != nil {
		t.Fatalf("GROUP BY...HAVING: %v", err)
	}
	defer rows.Close()

	got := map[string]float64{}
	for rows.Next() {
		var label string
		var total float64
		if err := rows.Scan(&label, &total); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[label] = total
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 1 || got["Kissing"] != 15 {
		t.Errorf("got %v, want only Kissing=15", got)
	}
}

// get_latest_scene_run orders by completed_at DESC NULLS LAST. NULLS LAST is not
// portable, so the port uses (x IS NULL) - prove it sorts as intended.
func TestTursoNullsLastEmulation(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	mustExecArgs(t, db, `INSERT INTO ai_model_runs (id, service, entity_id, completed_at, created_at) VALUES (1,'s',1,NULL,0)`)
	mustExecArgs(t, db, `INSERT INTO ai_model_runs (id, service, entity_id, completed_at, created_at) VALUES (2,'s',1,100,0)`)
	mustExecArgs(t, db, `INSERT INTO ai_model_runs (id, service, entity_id, completed_at, created_at) VALUES (3,'s',1,200,0)`)

	rows, err := db.Query(`SELECT id FROM ai_model_runs ORDER BY (completed_at IS NULL) ASC, completed_at DESC, id DESC`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	var order []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		order = append(order, id)
	}
	want := []int{3, 2, 1} // newest completed first, NULL last
	if fmt.Sprint(order) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", order, want)
	}
}

// The interactions pre-dedup query does `WHERE client_event_id IN (batch)`.
// SQLite's default variable limit is 999; Turso's is undocumented, so the port
// chunks at 500. Verify 600 works (so 500 is comfortably safe).
func TestTursoLargeInClause(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	const n = 600
	for i := 1; i <= 10; i++ {
		insertSpikeRun(t, db, "native", i)
	}

	args := make([]any, n)
	ph := make([]string, n)
	for i := 0; i < n; i++ {
		args[i] = i + 1
		ph[i] = "?"
	}
	q := `SELECT COUNT(*) FROM ai_model_runs WHERE entity_id IN (` + strings.Join(ph, ",") + `)`

	var count int
	if err := db.QueryRow(q, args...).Scan(&count); err != nil {
		t.Fatalf("%d-parameter IN: %v", n, err)
	}
	if count != 10 {
		t.Errorf("count = %d, want 10", count)
	}
}

// JSON columns are plain TEXT marshalled in Go - no json_*() SQL functions.
func TestTursoJSONTextRoundTrip(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	in := map[string]any{"frame_interval": 2.0, "vr": true, "skip": []any{"a", "b"}, "none": nil}
	blob, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var id int64
	if err := db.QueryRow(
		`INSERT INTO ai_model_runs (service, entity_id, input_params, created_at) VALUES (?,?,?,?) RETURNING id`,
		"native", 1, string(blob), 0,
	).Scan(&id); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var out sql.NullString
	if err := db.QueryRow(`SELECT input_params FROM ai_model_runs WHERE id = ?`, id).Scan(&out); err != nil {
		t.Fatalf("select: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(out.String), &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back["frame_interval"] != 2.0 || back["vr"] != true {
		t.Errorf("round-trip lost data: %#v", back)
	}
	if v, ok := back["none"]; !ok || v != nil {
		t.Errorf("null not preserved: %#v", back)
	}
}

// The ingest path writes a whole batch in one transaction and must roll back
// cleanly on error (the port deliberately avoids SAVEPOINTs).
func TestTursoTransactionRollback(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO ai_model_runs (service, entity_id, created_at) VALUES ('a',1,0)`); err != nil {
		t.Fatalf("insert in tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_model_runs`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rollback left %d rows", n)
	}
}

// Batched insert inside one transaction is the ingest hot path.
func TestTursoBatchInsert(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)
	runID := insertSpikeRun(t, db, "native", 1)

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO ai_result_aggregates
		(run_id, payload_type, str_value, metric, value_float) VALUES (?,?,?,?,?)`)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	const rows = 10000
	for i := 0; i < rows; i++ {
		if _, err := stmt.Exec(runID, "tag", fmt.Sprintf("label-%d", i), "duration_s", float64(i)); err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
	}
	if err := stmt.Close(); err != nil {
		t.Fatalf("stmt close: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_result_aggregates`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != rows {
		t.Errorf("inserted %d rows, want %d", n, rows)
	}
}

// The plan declares FKs for documentation but performs cascades explicitly in
// Go. This records which behaviour the engine actually gives us.
func TestTursoForeignKeyCascadeBehaviour(t *testing.T) {
	db := openSpike(t)
	mustExec(t, db, spikeSchema)

	runID := insertSpikeRun(t, db, "native", 1)
	mustExecArgs(t, db, `INSERT INTO ai_result_aggregates
		(run_id, payload_type, str_value, metric, value_float) VALUES (?,'tag','x','duration_s',1)`, runID)

	if _, err := db.Exec(`DELETE FROM ai_model_runs WHERE id = ?`, runID); err != nil {
		t.Fatalf("delete parent: %v", err)
	}

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ai_result_aggregates`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	t.Logf("orphaned child rows after parent delete: %d (0 = cascade enforced, 1 = not enforced)", n)
	if n != 0 {
		t.Log("FK cascade is NOT enforced by default - explicit cascading deletes in Go are required, as planned")
	}
}

func mustExec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.Exec(q); err != nil {
		t.Fatalf("exec %.60q: %v", q, err)
	}
}

func mustExecArgs(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %.60q: %v", q, err)
	}
}

func insertSpikeRun(t *testing.T, db *sql.DB, service string, entityID int) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(
		`INSERT INTO ai_model_runs (service, entity_id, created_at) VALUES (?,?,?) RETURNING id`,
		service, entityID, 0,
	).Scan(&id); err != nil {
		t.Fatalf("insert run: %v", err)
	}
	return id
}
