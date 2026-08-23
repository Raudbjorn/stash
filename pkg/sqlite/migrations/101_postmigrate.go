package migrations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/jmoiron/sqlx"
	"github.com/stashapp/stash/pkg/sqlite"
)

func post101(ctx context.Context, db *sqlx.DB) error {
	m := migrator{db: db}
	return m.withTxn(ctx, func(tx *sqlx.Tx) error {
		rows, err := tx.QueryContext(ctx, `
			SELECT id, object_filter
			FROM saved_filters
			WHERE mode = 'SCENES'
				AND object_filter IS NOT NULL
				AND object_filter <> ''
			ORDER BY id
		`)
		if err != nil {
			return fmt.Errorf("querying scene saved filters: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var (
				id  int
				raw []byte
			)
			if err := rows.Scan(&id, &raw); err != nil {
				return fmt.Errorf("scanning scene saved filter: %w", err)
			}

			filter, ok := decodeFilterObject(raw)
			if !ok || !removeInteractiveCriteria(filter) {
				continue
			}

			updated, err := json.Marshal(filter)
			if err != nil {
				return fmt.Errorf("encoding scene saved filter %d: %w", id, err)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE saved_filters SET object_filter = ? WHERE id = ?", updated, id); err != nil {
				return fmt.Errorf("updating scene saved filter %d: %w", id, err)
			}
		}

		return rows.Err()
	})
}

func decodeFilterObject(raw []byte) (map[string]interface{}, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()

	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, false
	}
	filter, ok := value.(map[string]interface{})
	return filter, ok
}

func removeInteractiveCriteria(filter map[string]interface{}) bool {
	changed := false
	for _, key := range []string{"interactive", "interactive_speed"} {
		if _, found := filter[key]; found {
			delete(filter, key)
			changed = true
		}
	}

	for _, key := range []string{"AND", "OR", "NOT", "file_filter", "files_filter", "video_file_filter"} {
		if removeInteractiveCriteriaFromValue(filter[key]) {
			changed = true
		}
	}

	return changed
}

func removeInteractiveCriteriaFromValue(value interface{}) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		return removeInteractiveCriteria(typed)
	case []interface{}:
		changed := false
		for _, item := range typed {
			if removeInteractiveCriteriaFromValue(item) {
				changed = true
			}
		}
		return changed
	default:
		return false
	}
}

func init() {
	sqlite.RegisterPostMigration(101, post101)
}
