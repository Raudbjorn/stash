package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// expectedTables is the full baseline. The Python server defines 19 tables:
// 18 across models/ plus task_history, which lives in tasks/history.py and is
// easy to miss when counting.
// latestMigrationVersion is read from the embedded migrations rather than
// hard-coded, so adding one does not silently break an unrelated assertion.
func latestMigrationVersion(t *testing.T) int {
	t.Helper()

	migrations, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations are embedded")
	}
	return migrations[len(migrations)-1].version
}

var expectedTables = []string{
	// Migration 0002 additions: writeback provenance, cached embeddings and
	// locally-fitted heads. All three are derived data kept out of Stash's own
	// database so its backup and schema stay untouched.
	"ai_marker_writeback",
	"ai_scene_embeddings",
	"ai_trained_heads",
	"ai_model_run_models",
	"ai_model_runs",
	"ai_models",
	"ai_result_aggregates",
	"ai_result_timespans",
	"image_derived",
	"interaction_events",
	"interaction_library_search",
	"interaction_session_aliases",
	"interaction_sessions",
	"plugin_catalog",
	"plugin_meta",
	"plugin_settings",
	"plugin_sources",
	"recommendation_preferences",
	"scene_derived",
	"scene_watch",
	"scene_watch_segments",
	"task_history",
}

// Every index the Python schema created, including those added by alembic 0004.
var expectedIndexes = []string{
	"ix_ai_aggregates_entity",
	"ix_ai_aggregates_payload",
	"ix_ai_aggregates_run_payload_metric",
	"ix_ai_model_runs_entity",
	"ix_ai_model_runs_service_entity",
	"ix_ai_models_service",
	"ix_ai_run_models_model",
	"ix_ai_run_models_run",
	"ix_ai_timespans_entity",
	"ix_ai_timespans_payload",
	"ix_ai_timespans_run",
	"ix_ai_timespans_run_payload",
	"ix_ai_timespans_start",
	"ix_interaction_client_ts",
	"ix_interaction_events_entity_id",
	"ix_interaction_events_event_type",
	"ix_interaction_events_session_entity_ts",
	"ix_interaction_library_search_library_created",
	"ix_interaction_library_search_session_id",
	"ix_interaction_session_aliases_canonical",
	"ix_interaction_session_scene",
	"ix_interaction_sessions_client_fingerprint",
	"ix_interaction_sessions_ended_at",
	"ix_interaction_sessions_fp_ended_last",
	"ix_interaction_sessions_session_id",
	"ix_plugin_catalog_plugin_name",
	"ix_plugin_catalog_source_id",
	"ix_plugin_settings_plugin_name",
	"ix_plugin_settings_plugin_name_key",
	"ix_scene_watch_page_entered",
	"ix_scene_watch_scene_id",
	"ix_scene_watch_segments_scene_id",
	"ix_scene_watch_segments_scene_start",
	"ix_scene_watch_segments_scene_watch_id",
	"ix_scene_watch_segments_sess_scene_start",
	"ix_scene_watch_segments_session_id",
	"ix_scene_watch_session_id",
	"ix_scene_watch_session_scene",
	"ix_task_history_created_at",
	"ix_task_history_service_status_created",
}

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func names(t *testing.T, db *DB, kind string) map[string]bool {
	t.Helper()
	rows, err := db.SQL().Query(
		`SELECT name FROM sqlite_master WHERE type = ? AND name NOT LIKE 'sqlite_%'`, kind)
	if err != nil {
		t.Fatalf("list %ss: %v", kind, err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[n] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return got
}

func TestMigrateCreatesFullSchema(t *testing.T) {
	db := openTestDB(t)

	tables := names(t, db, "table")
	for _, want := range expectedTables {
		if !tables[want] {
			t.Errorf("missing table %q", want)
		}
	}
	// schema_migrations is the ledger; everything else should be accounted for.
	for got := range tables {
		// schema_migrations is the ledger. plugin_migrations is created on
		// demand by the plugin migration runner, so a fresh database has
		// neither it nor any reason for it.
		if got == "schema_migrations" || got == "plugin_migrations" {
			continue
		}
		found := false
		for _, want := range expectedTables {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected table %q", got)
		}
	}

	indexes := names(t, db, "index")
	for _, want := range expectedIndexes {
		if !indexes[want] {
			t.Errorf("missing index %q", want)
		}
	}

	// 19 from the baseline plus the three migration 0002 added. Asserted so
	// that adding a table without listing it above cannot pass unnoticed.
	if n := len(expectedTables); n != 22 {
		t.Errorf("expected 22 tables across the migrations, listed %d", n)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ai.db")
	ctx := context.Background()

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	v1, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-opening must not attempt to re-apply anything.
	db2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer db2.Close()

	v2, err := db2.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("schema version: %v", err)
	}
	if v1 != v2 || v1 != latestMigrationVersion(t) {
		t.Errorf("schema version drifted: %d then %d, want %d both times",
			v1, v2, latestMigrationVersion(t))
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "ai.db")
	db, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open with missing parent directories: %v", err)
	}
	defer db.Close()
	if db.Path() != path {
		t.Errorf("Path() = %q, want %q", db.Path(), path)
	}
}

// STRICT is only worth declaring if it actually rejects bad writes.
func TestSchemaIsStrict(t *testing.T) {
	db := openTestDB(t)

	_, err := db.SQL().Exec(
		`INSERT INTO scene_watch (session_id, scene_id, page_entered_at, created_at)
		 VALUES (?,?,?,?)`,
		"sess", "not-an-integer", NowMillis(), NowMillis())
	if err == nil {
		t.Error("STRICT did not reject a TEXT value in scene_watch.scene_id")
	}
}

// task_history's id columns must be TEXT: the values are uuid hex and dotted
// action ids, which the original migration wrongly typed as INTEGER.
func TestTaskHistoryAcceptsStringIdentifiers(t *testing.T) {
	db := openTestDB(t)

	_, err := db.SQL().Exec(
		`INSERT INTO task_history (task_id, action_id, service, status, submitted_at, item_id, created_at)
		 VALUES (?,?,?,?,?,?,?)`,
		"3f2b1c9d4e5a6b7c", "skier_aitagging.tag_scenes", "AI_Tagging", "completed",
		1700000000.5, "scene-42", NowMillis())
	if err != nil {
		t.Fatalf("task_history rejected string identifiers: %v", err)
	}

	var taskID, actionID, itemID string
	var submitted float64
	err = db.SQL().QueryRow(
		`SELECT task_id, action_id, item_id, submitted_at FROM task_history`).
		Scan(&taskID, &actionID, &itemID, &submitted)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if taskID != "3f2b1c9d4e5a6b7c" || actionID != "skier_aitagging.tag_scenes" {
		t.Errorf("identifiers round-tripped wrong: %q %q", taskID, actionID)
	}
	// Epoch seconds as REAL, because the frontend reads these as numbers.
	if submitted != 1700000000.5 {
		t.Errorf("submitted_at = %v, want 1700000000.5", submitted)
	}
}

// Folded in from alembic 0003: ids like "evt_..." are not integers.
func TestClientEventIDIsText(t *testing.T) {
	db := openTestDB(t)

	_, err := db.SQL().Exec(
		`INSERT INTO interaction_events
		 (client_event_id, session_id, event_type, entity_type, entity_id, client_ts)
		 VALUES (?,?,?,?,?,?)`,
		"evt_9f3c2a", "sess-1", "scene_watch_start", "scene", 42, NowMillis())
	if err != nil {
		t.Fatalf("interaction_events rejected a textual client_event_id: %v", err)
	}
}

func TestTimestampRoundTrip(t *testing.T) {
	db := openTestDB(t)

	// Sub-second precision is the reason timestamps are integers rather than
	// SQLite's second-resolution CURRENT_TIMESTAMP text.
	want := time.Date(2026, 7, 26, 12, 34, 56, 789_000_000, time.UTC)

	if _, err := db.SQL().Exec(
		`INSERT INTO interaction_sessions
		 (session_id, last_event_ts, session_start_ts, updated_at, ended_at)
		 VALUES (?,?,?,?,?)`,
		"sess-1", ToMillis(want), ToMillis(want), ToMillis(want), nil,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var lastEvent int64
	var ended NullTime
	if err := db.SQL().QueryRow(
		`SELECT last_event_ts, ended_at FROM interaction_sessions`).
		Scan(&lastEvent, &ended); err != nil {
		t.Fatalf("scan: %v", err)
	}

	if got := FromMillis(lastEvent); !got.Equal(want) {
		t.Errorf("timestamp = %v, want %v", got, want)
	}
	if ended.Valid {
		t.Error("NULL timestamp scanned as valid")
	}
	if ended.TimePtr() != nil {
		t.Error("TimePtr on a NULL timestamp should be nil")
	}
}

func TestJSONColumnRoundTrip(t *testing.T) {
	db := openTestDB(t)

	meta := map[string]any{"position": 12.5, "duration": 1800.0, "vr": true}
	arg, err := MarshalArg(meta)
	if err != nil {
		t.Fatalf("MarshalArg: %v", err)
	}

	if _, err := db.SQL().Exec(
		`INSERT INTO interaction_events
		 (client_event_id, session_id, event_type, entity_type, entity_id, client_ts, metadata)
		 VALUES (?,?,?,?,?,?,?)`,
		"evt-1", "sess", "scene_watch_progress", "scene", 1, NowMillis(), arg,
	); err != nil {
		t.Fatalf("insert: %v", err)
	}
	// A second row with SQL NULL metadata, which must stay distinguishable.
	if _, err := db.SQL().Exec(
		`INSERT INTO interaction_events
		 (client_event_id, session_id, event_type, entity_type, entity_id, client_ts, metadata)
		 VALUES (?,?,?,?,?,?,?)`,
		"evt-2", "sess", "scene_view", "scene", 1, NowMillis(), nil,
	); err != nil {
		t.Fatalf("insert null: %v", err)
	}

	rows, err := db.SQL().Query(
		`SELECT client_event_id, metadata FROM interaction_events ORDER BY client_event_id`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()

	got := map[string]JSONText[map[string]any]{}
	for rows.Next() {
		var id string
		var j JSONText[map[string]any]
		if err := rows.Scan(&id, &j); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[id] = j
	}

	if !got["evt-1"].Valid {
		t.Fatal("evt-1 metadata scanned as NULL")
	}
	if got["evt-1"].Data["position"] != 12.5 || got["evt-1"].Data["vr"] != true {
		t.Errorf("metadata round-trip lost data: %#v", got["evt-1"].Data)
	}
	if got["evt-2"].Valid {
		t.Error("evt-2 metadata should be SQL NULL, not a JSON value")
	}
}

func TestNormalizeNullStrings(t *testing.T) {
	in := map[string]any{
		"real":   "value",
		"nulled": "null",
		"upper":  "NULL",
		"spaced": "  none  ",
		"nested": map[string]any{"deep": "undefined", "keep": 3.0},
		"list":   []any{"nil", "ok"},
		"number": 42.0,
	}

	out, ok := NormalizeNullStrings(in).(map[string]any)
	if !ok {
		t.Fatal("expected a map back")
	}

	for _, key := range []string{"nulled", "upper", "spaced"} {
		if out[key] != nil {
			t.Errorf("%s = %#v, want nil", key, out[key])
		}
	}
	if out["real"] != "value" || out["number"] != 42.0 {
		t.Errorf("real values were altered: %#v", out)
	}

	nested := out["nested"].(map[string]any)
	if nested["deep"] != nil || nested["keep"] != 3.0 {
		t.Errorf("nested normalisation wrong: %#v", nested)
	}
	list := out["list"].([]any)
	if list[0] != nil || list[1] != "ok" {
		t.Errorf("list normalisation wrong: %#v", list)
	}
}

func TestChunkingHelpers(t *testing.T) {
	ids := make([]int, 1200)
	for i := range ids {
		ids[i] = i
	}

	batches := ChunkInts(ids, 500)
	if len(batches) != 3 {
		t.Fatalf("got %d batches, want 3", len(batches))
	}
	if len(batches[0]) != 500 || len(batches[2]) != 200 {
		t.Errorf("batch sizes = %d/%d/%d", len(batches[0]), len(batches[1]), len(batches[2]))
	}
	if ChunkInts(nil, 500) != nil {
		t.Error("empty input should produce no batches")
	}

	if got := Placeholders(3); got != "?,?,?" {
		t.Errorf("Placeholders(3) = %q", got)
	}
	if got := Placeholders(0); got != "" {
		t.Errorf("Placeholders(0) = %q", got)
	}

	if n := len(ChunkStrings(make([]string, 501), 500)); n != 2 {
		t.Errorf("ChunkStrings produced %d batches, want 2", n)
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	sentinel := context.Canceled
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scene_derived (scene_id, view_count) VALUES (1, 5)`); err != nil {
			return err
		}
		return sentinel
	})
	if err != sentinel {
		t.Fatalf("InTx returned %v, want the callback's error", err)
	}

	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM scene_derived`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("rollback left %d rows", n)
	}
}
