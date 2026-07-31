package store

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestValidatePluginSQLNamespacing(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		readOnly bool
		wantErr  bool
	}{
		{"own table select", "SELECT * FROM p_demo_items", true, false},
		{"own table quoted", `SELECT * FROM "p_demo_items"`, true, false},
		{"own table bracketed", "SELECT * FROM [p_demo_items]", true, false},
		{"case insensitive prefix", "SELECT * FROM P_DEMO_Items", true, false},
		{"join within namespace", "SELECT a.x FROM p_demo_a a JOIN p_demo_b b ON a.id = b.id", true, false},
		{"unambiguous namespace", "SELECT * FROM p_demo__multi_word", true, false},

		// The whole point: a plugin must not reach the server's tables, another
		// plugin's tables, or Stash's.
		{"core table", "SELECT * FROM task_history", true, true},
		{"another plugin", "SELECT * FROM p_other_items", true, true},
		{"settings table", "SELECT value FROM settings", true, true},
		{"join escapes namespace", "SELECT * FROM p_demo_a JOIN interaction_sessions s ON 1=1", true, true},
		{"insert elsewhere", "INSERT INTO ai_results (id) VALUES (1)", false, true},
		{"update elsewhere", "UPDATE scene_watch SET x = 1", false, true},
		{"create outside namespace", "CREATE TABLE evil (id INTEGER)", false, true},

		// ATTACH would open Stash's own database file and walk around the rule
		// entirely, so it is refused regardless of what it names.
		{"attach", "ATTACH DATABASE 'stash-go.sqlite' AS stash", false, true},
		{"pragma", "PRAGMA table_list", false, true},

		{"multiple statements", "SELECT 1 FROM p_demo_a; DROP TABLE p_demo_a", true, true},
		{"write rejected on read path", "DELETE FROM p_demo_items", true, true},
		{"write allowed on write path", "DELETE FROM p_demo_items WHERE id = ?", false, false},
		{"empty", "   ", true, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePluginSQL("demo", tc.query, 0, tc.readOnly)
			if tc.wantErr && err == nil {
				t.Errorf("ValidatePluginSQL accepted %q", tc.query)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidatePluginSQL rejected %q: %v", tc.query, err)
			}
		})
	}
}

func TestValidatePluginSQLRejectsNamespaceBypasses(t *testing.T) {
	for name, test := range map[string]struct {
		plugin string
		query  string
	}{
		"comma join": {
			plugin: "mine",
			query:  "SELECT * FROM p_mine_items, plugins",
		},
		"prefix confusion": {
			plugin: "foo",
			query:  "SELECT * FROM p_foo_bar_secrets",
		},
		"name collision": {
			plugin: "a-b",
			query:  "SELECT * FROM p_a_b_t",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePluginSQL(test.plugin, test.query, 0, true); err == nil {
				t.Fatalf("ValidatePluginSQL accepted namespace bypass %q", test.query)
			}
		})
	}
}

// A semicolon or a table name inside a string literal is data, not SQL. Getting
// this wrong in either direction is bad: refusing legitimate queries, or
// letting a crafted literal hide a reference.
func TestValidatePluginSQLIgnoresStringLiterals(t *testing.T) {
	if err := ValidatePluginSQL("demo", "SELECT * FROM p_demo_x WHERE name = 'a;b'", 0, true); err != nil {
		t.Errorf("rejected a legitimate literal containing a semicolon: %v", err)
	}
	if err := ValidatePluginSQL("demo", "SELECT * FROM p_demo_x WHERE note = 'from task_history'", 0, true); err != nil {
		t.Errorf("a table name inside a literal was treated as a reference: %v", err)
	}
	if err := ValidatePluginSQL("demo", "SELECT * FROM p_demo_x -- FROM task_history", 0, true); err != nil {
		t.Errorf("a table name inside a comment was treated as a reference: %v", err)
	}
	// A comment must not be able to hide a real statement separator either.
	if err := ValidatePluginSQL("demo", "SELECT 1 FROM p_demo_x /* ; */ ", 0, true); err != nil {
		t.Errorf("a comment containing a semicolon was miscounted: %v", err)
	}

	for _, query := range []string{
		"SELECT '--'; DROP TABLE task_history;",
		"SELECT '/*'; DROP TABLE task_history;",
		`SELECT "--" FROM p_demo_items; DROP TABLE task_history;`,
	} {
		if err := ValidatePluginSQL("demo", query, 0, false); !errors.Is(err, ErrPluginSQLMultiple) {
			t.Errorf("comment marker bypass %q = %v, want ErrPluginSQLMultiple", query, err)
		}
	}
}

func TestPluginExecuteRejectsCommentMarkerStatementBypass(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	_, err := db.PluginExecute(ctx, "demo", "SELECT '--'; DROP TABLE task_history;", nil)
	if !errors.Is(err, ErrPluginSQLMultiple) {
		t.Fatalf("PluginExecute = %v, want ErrPluginSQLMultiple", err)
	}
	if _, err := db.SQL().ExecContext(ctx, "SELECT COUNT(*) FROM task_history"); err != nil {
		t.Fatalf("protected table was dropped: %v", err)
	}
}

func TestValidatePluginSQLCapsParameters(t *testing.T) {
	err := ValidatePluginSQL("demo", "SELECT * FROM p_demo_x", MaxPluginParams+1, true)
	if !errors.Is(err, ErrPluginSQLTooManyArgs) {
		t.Errorf("err = %v, want ErrPluginSQLTooManyArgs", err)
	}
}

// The error has to name the rule, because the plugin author sees it and has to
// act on it.
func TestPluginTableErrorIsActionable(t *testing.T) {
	err := ValidatePluginSQL("demo", "SELECT * FROM settings", 0, true)
	var tableErr ErrPluginTable
	if !errors.As(err, &tableErr) {
		t.Fatalf("err = %v, want ErrPluginTable", err)
	}
	if !strings.Contains(err.Error(), "p_demo_") {
		t.Errorf("error does not name the required prefix: %s", err)
	}
}

func TestPluginQueryAndExecute(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.SQL().ExecContext(ctx,
		`CREATE TABLE p_demo_items (id INTEGER PRIMARY KEY, name TEXT) STRICT`); err != nil {
		t.Fatal(err)
	}

	affected, err := db.PluginExecute(ctx, "demo",
		"INSERT INTO p_demo_items (id, name) VALUES (?, ?)", []any{1, "first"})
	if err != nil {
		t.Fatalf("PluginExecute: %v", err)
	}
	if affected != 1 {
		t.Errorf("affected = %d, want 1", affected)
	}

	rows, err := db.PluginQuery(ctx, "demo", "SELECT id, name FROM p_demo_items", nil)
	if err != nil {
		t.Fatalf("PluginQuery: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	// Text must arrive as a string, not as base64-encoded bytes.
	if rows[0]["name"] != "first" {
		t.Errorf("name = %#v, want %q", rows[0]["name"], "first")
	}

	// An empty result must be an empty list, never null: the plugin iterates it.
	rows, err = db.PluginQuery(ctx, "demo", "SELECT id FROM p_demo_items WHERE id = ?", []any{99})
	if err != nil {
		t.Fatalf("PluginQuery: %v", err)
	}
	if rows == nil || len(rows) != 0 {
		t.Errorf("rows = %#v, want an empty slice", rows)
	}
}

func TestPluginQueryRefusesForeignTables(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.PluginQuery(context.Background(), "demo", "SELECT * FROM settings", nil); err == nil {
		t.Fatal("PluginQuery read a table outside the plugin's namespace")
	}
}

func TestPluginExecuteBatchIsAtomic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.SQL().ExecContext(ctx,
		`CREATE TABLE p_demo_items (id INTEGER PRIMARY KEY, name TEXT) STRICT`); err != nil {
		t.Fatal(err)
	}

	total, err := db.PluginExecuteBatch(ctx, "demo",
		[]string{
			"INSERT INTO p_demo_items (id, name) VALUES (?, ?)",
			"INSERT INTO p_demo_items (id, name) VALUES (?, ?)",
		},
		[][]any{{1, "a"}, {2, "b"}})
	if err != nil {
		t.Fatalf("PluginExecuteBatch: %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}

	// A failure part-way through must roll the whole batch back, and validation
	// must happen before anything is written at all.
	_, err = db.PluginExecuteBatch(ctx, "demo",
		[]string{
			"INSERT INTO p_demo_items (id, name) VALUES (?, ?)",
			"INSERT INTO task_history (task_id) VALUES (?)",
		},
		[][]any{{3, "c"}, {"x"}})
	if err == nil {
		t.Fatal("PluginExecuteBatch accepted a foreign table")
	}

	rows, err := db.PluginQuery(ctx, "demo", "SELECT id FROM p_demo_items WHERE id = ?", []any{3})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Error("a rejected batch wrote its first statement")
	}
}
