package migrations

import (
	"context"
	"testing"

	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPost101SanitizesNestedSceneFilters(t *testing.T) {
	db, err := sqlx.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE saved_filters (
			id INTEGER PRIMARY KEY,
			mode TEXT NOT NULL,
			object_filter BLOB
		)
	`)
	require.NoError(t, err)

	nested := `{
		"interactive": true,
		"title": {"value": "keep"},
		"AND": {
			"interactive_speed": {"value": 42},
			"OR": {
				"file_filter": {
					"interactive": false,
					"path": {"value": "keep"}
				},
				"NOT": {
					"interactive": true,
					"tags": {"value": [1, 2]}
				}
			}
		}
	}`
	unrelated := `{ "title": { "value": "unchanged" }, "rating100": { "value": 80 } }`
	invalid := `{invalid`
	nonScene := `{ "interactive": true }`
	arrayLogical := `{ "OR": [{ "interactive": true }, { "interactive_speed": { "value": 10 }, "code": { "value": "keep" } }] }`

	fixtures := []struct {
		id     int
		mode   string
		filter string
	}{
		{1, "SCENES", nested},
		{2, "SCENES", unrelated},
		{3, "SCENES", invalid},
		{4, "IMAGES", nonScene},
		{5, "SCENES", arrayLogical},
	}
	for _, fixture := range fixtures {
		_, err := db.Exec("INSERT INTO saved_filters (id, mode, object_filter) VALUES (?, ?, ?)", fixture.id, fixture.mode, fixture.filter)
		require.NoError(t, err)
	}

	require.NoError(t, post101(context.Background(), db))

	got := make(map[int]string)
	rows, err := db.Query("SELECT id, object_filter FROM saved_filters ORDER BY id")
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var (
			id     int
			filter string
		)
		require.NoError(t, rows.Scan(&id, &filter))
		got[id] = filter
	}
	require.NoError(t, rows.Err())

	assert.JSONEq(t, `{
		"title": {"value": "keep"},
		"AND": {
			"OR": {
				"file_filter": {"path": {"value": "keep"}},
				"NOT": {"tags": {"value": [1, 2]}}
			}
		}
	}`, got[1])
	assert.Equal(t, unrelated, got[2], "unrelated valid JSON must remain byte-for-byte unchanged")
	assert.Equal(t, invalid, got[3], "invalid JSON must remain unchanged")
	assert.Equal(t, nonScene, got[4], "non-scene filters must remain unchanged")
	assert.JSONEq(t, `{ "OR": [{}, { "code": { "value": "keep" } }] }`, got[5])
}
