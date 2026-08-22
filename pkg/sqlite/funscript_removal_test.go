//go:build integration
// +build integration

package sqlite_test

import (
	"database/sql"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFunscriptSchemaRemoved(t *testing.T) {
	rawDB, err := sql.Open("sqlite3", db.DatabasePath())
	require.NoError(t, err)
	defer rawDB.Close()

	var tableCount int
	err = rawDB.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = 'funscript_index'
	`).Scan(&tableCount)
	require.NoError(t, err)
	assert.Zero(t, tableCount, "funscript_index table must be removed")

	rows, err := rawDB.Query("PRAGMA table_info(video_files)")
	require.NoError(t, err)
	defer rows.Close()

	columns := make(map[string]struct{})
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		require.NoError(t, rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey))
		columns[name] = struct{}{}
	}
	require.NoError(t, rows.Err())
	assert.NotContains(t, columns, "interactive")
	assert.NotContains(t, columns, "interactive_speed")
}
