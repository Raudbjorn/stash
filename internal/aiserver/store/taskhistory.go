package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Task history is a bounded log of terminal top-level tasks, shown in the UI's
// task dashboard. Children of a fan-out batch are excluded so a 500-chunk job
// appears as one row rather than 501.

// History pruning thresholds, matching the Python original: once the table
// exceeds historyHighWater rows it is trimmed back to historyLowWater, so the
// cost is amortised rather than paid on every insert.
const (
	historyHighWater = 600
	historyLowWater  = 500
)

// TaskHistoryEntry is one persisted task.
//
// SubmittedAt/StartedAt/FinishedAt are epoch SECONDS as REAL, unlike every
// other timestamp in this schema, because the frontend reads them as plain
// numbers and sorts on them.
type TaskHistoryEntry struct {
	TaskID      string         `json:"task_id"`
	ActionID    string         `json:"action_id"`
	Service     string         `json:"service"`
	Status      string         `json:"status"`
	SubmittedAt float64        `json:"submitted_at"`
	StartedAt   *float64       `json:"started_at"`
	FinishedAt  *float64       `json:"finished_at"`
	DurationMS  *int64         `json:"duration_ms"`
	ItemsSent   *int64         `json:"items_sent"`
	ItemID      *string        `json:"item_id"`
	Error       *string        `json:"error"`
	InputParams map[string]any `json:"input_params,omitempty"`
	Result      any            `json:"result,omitempty"`
}

// InsertTaskHistory records a terminal task, ignoring a task already present.
//
// Insertion and pruning share one transaction so the table cannot be observed
// over its high-water mark.
func (db *DB) InsertTaskHistory(ctx context.Context, e TaskHistoryEntry) error {
	paramsJSON, err := marshalTaskHistoryValue(e.InputParams)
	if err != nil {
		return fmt.Errorf("encode task history parameters for %s: %w", e.TaskID, err)
	}
	resultJSON, err := marshalTaskHistoryValue(e.Result)
	if err != nil {
		return fmt.Errorf("encode task history result for %s: %w", e.TaskID, err)
	}

	return db.InTx(ctx, func(tx *sql.Tx) error {
		var exists int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM task_history WHERE task_id = ?`, e.TaskID).Scan(&exists)
		if err == nil {
			return nil // already recorded
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check task history for %s: %w", e.TaskID, err)
		}

		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_history
			 (task_id, action_id, service, status, submitted_at, started_at, finished_at,
			  duration_ms, items_sent, item_id, error, input_params, result_json, created_at)
			 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			e.TaskID, e.ActionID, e.Service, e.Status,
			e.SubmittedAt, e.StartedAt, e.FinishedAt,
			e.DurationMS, e.ItemsSent, e.ItemID, e.Error, paramsJSON, resultJSON, NowMillis(),
		); err != nil {
			return fmt.Errorf("insert task history for %s: %w", e.TaskID, err)
		}

		return pruneTaskHistory(ctx, tx)
	})
}

// pruneTaskHistory trims the oldest rows once the table grows past the high
// water mark.
func pruneTaskHistory(ctx context.Context, tx *sql.Tx) error {
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_history`).Scan(&total); err != nil {
		return fmt.Errorf("count task history: %w", err)
	}
	if total <= historyHighWater {
		return nil
	}

	overflow := total - historyLowWater
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM task_history WHERE id IN (
		     SELECT id FROM task_history ORDER BY created_at ASC, id ASC LIMIT ?
		 )`, overflow); err != nil {
		return fmt.Errorf("prune task history: %w", err)
	}
	return nil
}

// TaskHistoryFilter narrows a history query.
type TaskHistoryFilter struct {
	Service string
	Status  string
	// Limit caps the result set. Zero means 50; the maximum is 500, matching
	// the API's own clamp.
	Limit int
}

// ListTaskHistory returns recent history, newest first.
func (db *DB) ListTaskHistory(ctx context.Context, f TaskHistoryFilter) ([]TaskHistoryEntry, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	query := `SELECT task_id, action_id, service, status, submitted_at, started_at,
	                 finished_at, duration_ms, items_sent, item_id, error,
	                 input_params, result_json
	          FROM task_history`
	var args []any
	var where []string

	if f.Service != "" {
		where = append(where, "service = ?")
		args = append(args, f.Service)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	for i, clause := range where {
		if i == 0 {
			query += " WHERE " + clause
		} else {
			query += " AND " + clause
		}
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list task history: %w", err)
	}
	defer rows.Close()

	// Never nil: the endpoint must serialise as [] rather than null.
	out := []TaskHistoryEntry{}
	for rows.Next() {
		var e TaskHistoryEntry
		var started, finished sql.NullFloat64
		var duration, items sql.NullInt64
		var itemID, errText, paramsJSON, resultJSON sql.NullString

		if err := rows.Scan(&e.TaskID, &e.ActionID, &e.Service, &e.Status,
			&e.SubmittedAt, &started, &finished, &duration, &items, &itemID, &errText,
			&paramsJSON, &resultJSON); err != nil {
			return nil, fmt.Errorf("scan task history: %w", err)
		}

		e.StartedAt = Float64Ptr(started)
		e.FinishedAt = Float64Ptr(finished)
		e.DurationMS = Int64Ptr(duration)
		e.ItemsSent = Int64Ptr(items)
		e.ItemID = StringPtr(itemID)
		e.Error = StringPtr(errText)
		if paramsJSON.Valid {
			if err := json.Unmarshal([]byte(paramsJSON.String), &e.InputParams); err != nil {
				return nil, fmt.Errorf("decode task history parameters for %s: %w", e.TaskID, err)
			}
		}
		if resultJSON.Valid {
			if err := json.Unmarshal([]byte(resultJSON.String), &e.Result); err != nil {
				return nil, fmt.Errorf("decode task history result for %s: %w", e.TaskID, err)
			}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task history: %w", err)
	}
	return out, nil
}

func marshalTaskHistoryValue(value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return string(encoded), nil
}

// CountTaskHistory reports the number of stored rows, for tests and diagnostics.
func (db *DB) CountTaskHistory(ctx context.Context) (int, error) {
	var n int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM task_history`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count task history: %w", err)
	}
	return n, nil
}
