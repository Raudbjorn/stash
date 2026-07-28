package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Scene watch summaries, their segments, and the per-entity derived counters
// the recommendation engine reads.

// ErrWatchNotFound is returned when a (session, scene) pair has no watch row.
var ErrWatchNotFound = errors.New("scene watch not found")

const watchSelect = `SELECT id, session_id, scene_id, page_entered_at, page_left_at,
                            total_watched_s, watch_percent
                     FROM scene_watch`

// GetSceneWatch loads the watch row for a (session, scene) pair.
func (db *DB) GetSceneWatch(ctx context.Context, sessionID string, sceneID int) (SceneWatch, error) {
	var w SceneWatch
	var pageEnteredAt int64
	var pageLeftAt NullTime
	var watchPercent sql.NullFloat64

	err := db.sql.QueryRowContext(ctx, watchSelect+` WHERE session_id = ? AND scene_id = ?`,
		sessionID, sceneID).
		Scan(&w.ID, &w.SessionID, &w.SceneID, &pageEnteredAt, &pageLeftAt,
			&w.TotalWatchedS, &watchPercent)
	if errors.Is(err, sql.ErrNoRows) {
		return SceneWatch{}, ErrWatchNotFound
	}
	if err != nil {
		return SceneWatch{}, fmt.Errorf("get scene watch: %w", err)
	}

	w.PageEnteredAt = FromMillis(pageEnteredAt)
	w.PageLeftAt = pageLeftAt.TimePtr()
	w.WatchPercent = Float64Ptr(watchPercent)
	return w, nil
}

// CreateSceneWatch inserts a watch row and returns its id.
func (db *DB) CreateSceneWatch(ctx context.Context, w SceneWatch) (int64, error) {
	var id int64
	err := db.sql.QueryRowContext(ctx,
		`INSERT INTO scene_watch
		 (session_id, scene_id, page_entered_at, page_left_at, total_watched_s, watch_percent, created_at)
		 VALUES (?,?,?,?,?,?,?) RETURNING id`,
		w.SessionID, w.SceneID, ToMillis(w.PageEnteredAt), MillisPtr(w.PageLeftAt),
		w.TotalWatchedS, floatArg(w.WatchPercent), NowMillis(),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create scene watch: %w", err)
	}
	return id, nil
}

// UpdateSceneWatch persists the mutable parts of a watch row.
func (db *DB) UpdateSceneWatch(ctx context.Context, w SceneWatch) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE scene_watch
		 SET page_entered_at = ?, page_left_at = ?, total_watched_s = ?, watch_percent = ?
		 WHERE id = ?`,
		ToMillis(w.PageEnteredAt), MillisPtr(w.PageLeftAt),
		w.TotalWatchedS, floatArg(w.WatchPercent), w.ID)
	if err != nil {
		return fmt.Errorf("update scene watch: %w", err)
	}
	return nil
}

func floatArg(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// ------------------------------------------------------------- segments ---

// ListSceneSegments returns a pair's stored segments, ordered by start.
func (db *DB) ListSceneSegments(ctx context.Context, sessionID string, sceneID int) ([]SceneWatchSegment, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, scene_watch_id, session_id, scene_id, start_s, end_s, watched_s
		 FROM scene_watch_segments
		 WHERE session_id = ? AND scene_id = ?
		 ORDER BY start_s ASC, id ASC`, sessionID, sceneID)
	if err != nil {
		return nil, fmt.Errorf("list scene segments: %w", err)
	}
	defer rows.Close()

	var out []SceneWatchSegment
	for rows.Next() {
		var s SceneWatchSegment
		if err := rows.Scan(&s.ID, &s.SceneWatchID, &s.SessionID, &s.SceneID,
			&s.StartS, &s.EndS, &s.WatchedS); err != nil {
			return nil, fmt.Errorf("scan scene segment: %w", err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scene segments: %w", err)
	}
	return out, nil
}

// InsertSceneSegment stores a new segment and returns its id.
func (db *DB) InsertSceneSegment(ctx context.Context, s SceneWatchSegment) (int64, error) {
	var id int64
	err := db.sql.QueryRowContext(ctx,
		`INSERT INTO scene_watch_segments
		 (scene_watch_id, session_id, scene_id, start_s, end_s, watched_s, created_at)
		 VALUES (?,?,?,?,?,?,?) RETURNING id`,
		s.SceneWatchID, s.SessionID, s.SceneID, s.StartS, s.EndS, s.WatchedS, NowMillis(),
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert scene segment: %w", err)
	}
	return id, nil
}

// UpdateSceneSegment resizes an existing segment in place, preserving its id.
func (db *DB) UpdateSceneSegment(ctx context.Context, s SceneWatchSegment) error {
	_, err := db.sql.ExecContext(ctx,
		`UPDATE scene_watch_segments SET start_s = ?, end_s = ?, watched_s = ? WHERE id = ?`,
		s.StartS, s.EndS, s.WatchedS, s.ID)
	if err != nil {
		return fmt.Errorf("update scene segment: %w", err)
	}
	return nil
}

// DeleteSceneSegments removes segments by id.
func (db *DB) DeleteSceneSegments(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}

	intIDs := make([]int, len(ids))
	for i, id := range ids {
		intIDs[i] = int(id)
	}

	for _, batch := range ChunkInts(intIDs, maxSQLParams) {
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}
		if _, err := db.sql.ExecContext(ctx,
			`DELETE FROM scene_watch_segments WHERE id IN (`+Placeholders(len(batch))+`)`,
			args...); err != nil {
			return fmt.Errorf("delete scene segments: %w", err)
		}
	}
	return nil
}

// -------------------------------------------------------------- derived ---

// DerivedCounters are the per-entity aggregates the recommenders read.
type DerivedCounters struct {
	EntityID     int
	LastViewedAt *time.Time
	// DerivedOCount is incremented only when a session that ended on this
	// entity is finalised, never during ordinary ingest.
	DerivedOCount int
	ViewCount     int
}

// BumpSceneViews adds view counts and advances last_viewed_at for scenes
// touched by a batch. Rows are created when absent.
func (db *DB) BumpSceneViews(ctx context.Context, counts map[int]int, lastViewed map[int]time.Time, touched []int) error {
	return db.bumpViews(ctx, "scene_derived", "scene_id", counts, lastViewed, touched)
}

// BumpImageViews is BumpSceneViews for images.
func (db *DB) BumpImageViews(ctx context.Context, counts map[int]int, lastViewed map[int]time.Time, touched []int) error {
	return db.bumpViews(ctx, "image_derived", "image_id", counts, lastViewed, touched)
}

func (db *DB) bumpViews(ctx context.Context, table, idColumn string, counts map[int]int, lastViewed map[int]time.Time, touched []int) error {
	if len(touched) == 0 {
		return nil
	}

	return db.InTx(ctx, func(tx *sql.Tx) error {
		for _, id := range touched {
			inc := counts[id]

			var existing int
			err := tx.QueryRowContext(ctx,
				`SELECT 1 FROM `+table+` WHERE `+idColumn+` = ?`, id).Scan(&existing)

			switch {
			case errors.Is(err, sql.ErrNoRows):
				var lastArg any
				if lv, ok := lastViewed[id]; ok {
					lastArg = ToMillis(lv)
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO `+table+` (`+idColumn+`, last_viewed_at, derived_o_count, view_count)
					 VALUES (?,?,0,?)`, id, lastArg, inc); err != nil {
					return fmt.Errorf("create %s row: %w", table, err)
				}

			case err != nil:
				return fmt.Errorf("look up %s row: %w", table, err)

			default:
				if inc > 0 {
					if _, err := tx.ExecContext(ctx,
						`UPDATE `+table+` SET view_count = view_count + ? WHERE `+idColumn+` = ?`,
						inc, id); err != nil {
						return fmt.Errorf("increment %s view count: %w", table, err)
					}
				}
				// Only advance the timestamp; a late-arriving older event must
				// not move it backwards.
				if lv, ok := lastViewed[id]; ok {
					if _, err := tx.ExecContext(ctx,
						`UPDATE `+table+` SET last_viewed_at = ?
						 WHERE `+idColumn+` = ? AND (last_viewed_at IS NULL OR last_viewed_at < ?)`,
						ToMillis(lv), id, ToMillis(lv)); err != nil {
						return fmt.Errorf("update %s last viewed: %w", table, err)
					}
				}
			}
		}
		return nil
	})
}

// CreditDerivedOCount increments derived_o_count for entities a finalised
// session ended on.
//
// Note the asymmetry, preserved from the original: when the row does not exist
// yet it is created with view_count set to the same increment, even though the
// update path touches only derived_o_count. That is almost certainly
// unintentional, but the counters it produced are what existing installations
// hold, so it is reproduced rather than corrected.
func (db *DB) CreditDerivedOCount(ctx context.Context, sceneCounts, imageCounts map[int]int) error {
	if len(sceneCounts) == 0 && len(imageCounts) == 0 {
		return nil
	}

	return db.InTx(ctx, func(tx *sql.Tx) error {
		if err := creditTable(ctx, tx, "scene_derived", "scene_id", sceneCounts); err != nil {
			return err
		}
		return creditTable(ctx, tx, "image_derived", "image_id", imageCounts)
	})
}

func creditTable(ctx context.Context, tx *sql.Tx, table, idColumn string, counts map[int]int) error {
	for id, inc := range counts {
		if inc == 0 {
			continue
		}

		res, err := tx.ExecContext(ctx,
			`UPDATE `+table+` SET derived_o_count = derived_o_count + ? WHERE `+idColumn+` = ?`,
			inc, id)
		if err != nil {
			return fmt.Errorf("increment %s derived count: %w", table, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("check %s update: %w", table, err)
		}
		if affected > 0 {
			continue
		}

		// See the note on CreditDerivedOCount: view_count mirrors the increment
		// on creation only.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO `+table+` (`+idColumn+`, last_viewed_at, derived_o_count, view_count)
			 VALUES (?,NULL,?,?)`, id, inc, inc); err != nil {
			return fmt.Errorf("create %s row: %w", table, err)
		}
	}
	return nil
}

// GetSceneDerived reads a scene's counters, for tests and the API.
func (db *DB) GetSceneDerived(ctx context.Context, sceneID int) (DerivedCounters, error) {
	return db.getDerived(ctx, "scene_derived", "scene_id", sceneID)
}

// GetImageDerived reads an image's counters.
func (db *DB) GetImageDerived(ctx context.Context, imageID int) (DerivedCounters, error) {
	return db.getDerived(ctx, "image_derived", "image_id", imageID)
}

func (db *DB) getDerived(ctx context.Context, table, idColumn string, id int) (DerivedCounters, error) {
	var d DerivedCounters
	var lastViewed NullTime

	err := db.sql.QueryRowContext(ctx,
		`SELECT `+idColumn+`, last_viewed_at, derived_o_count, view_count FROM `+table+
			` WHERE `+idColumn+` = ?`, id).
		Scan(&d.EntityID, &lastViewed, &d.DerivedOCount, &d.ViewCount)
	if errors.Is(err, sql.ErrNoRows) {
		return DerivedCounters{EntityID: id}, nil
	}
	if err != nil {
		return DerivedCounters{}, fmt.Errorf("get %s: %w", table, err)
	}
	d.LastViewedAt = lastViewed.TimePtr()
	return d, nil
}

// --------------------------------------------------------- library search ---

// InsertLibrarySearch records a library search or filter event.
func (db *DB) InsertLibrarySearch(ctx context.Context, sessionID, library string, query *string, filters any) error {
	filtersArg, err := MarshalArg(filters)
	if err != nil {
		return err
	}
	if _, err := db.sql.ExecContext(ctx,
		`INSERT INTO interaction_library_search (session_id, library, query, filters, created_at)
		 VALUES (?,?,?,?,?)`,
		sessionID, library, NullString(query), filtersArg, NowMillis()); err != nil {
		return fmt.Errorf("insert library search: %w", err)
	}
	return nil
}
