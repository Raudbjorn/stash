package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Data access for the interaction pipeline: the raw event stream, the sessions
// events are grouped into, and the per-scene watch summaries derived from them.

// InteractionEvent is a stored event.
type InteractionEvent struct {
	ID            int64
	ClientEventID *string
	SessionID     string
	EventType     string
	EntityType    string
	EntityID      int
	ClientTS      time.Time
	Metadata      map[string]any
}

// InteractionSession groups a client's events over time.
type InteractionSession struct {
	ID                int64
	SessionID         string
	LastEventTS       time.Time
	SessionStartTS    time.Time
	LastEntityType    *string
	LastEntityID      *int
	LastEntityEventTS *time.Time
	ClientFingerprint *string
	EndedAt           *time.Time
}

// SceneWatch is one session's engagement with one scene.
type SceneWatch struct {
	ID            int64
	SessionID     string
	SceneID       int
	PageEnteredAt time.Time
	PageLeftAt    *time.Time
	TotalWatchedS float64
	WatchPercent  *float64
}

// SceneWatchSegment is a contiguous watched span.
type SceneWatchSegment struct {
	ID           int64
	SceneWatchID int64
	SessionID    string
	SceneID      int
	StartS       float64
	EndS         float64
	WatchedS     float64
}

// ---------------------------------------------------------------- events ---

// ExistingClientEventIDs returns which of the given client event ids are already
// stored, so a batch can be deduplicated with one query instead of one per
// event. Chunked because the parameter limit is not generous.
func (db *DB) ExistingClientEventIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	out := make(map[string]bool, len(ids))

	for _, batch := range ChunkStrings(ids, maxSQLParams) {
		args := make([]any, len(batch))
		for i, id := range batch {
			args[i] = id
		}

		query := `SELECT client_event_id FROM interaction_events
		          WHERE client_event_id IN (` + Placeholders(len(batch)) + `)`
		rows, err := db.sql.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("look up existing client event ids: %w", err)
		}

		for rows.Next() {
			var id sql.NullString
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan client event id: %w", err)
			}
			if id.Valid {
				out[id.String] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, fmt.Errorf("iterate client event ids: %w", err)
		}
	}
	return out, nil
}

// InsertEvent stores one event and returns its id.
func InsertEvent(ctx context.Context, tx *sql.Tx, e InteractionEvent) (int64, error) {
	meta, err := MarshalArg(e.Metadata)
	if err != nil {
		return 0, err
	}

	var id int64
	err = tx.QueryRowContext(ctx,
		`INSERT INTO interaction_events
		 (client_event_id, session_id, event_type, entity_type, entity_id, client_ts, metadata)
		 VALUES (?,?,?,?,?,?,?) RETURNING id`,
		NullString(e.ClientEventID), e.SessionID, e.EventType, e.EntityType,
		e.EntityID, ToMillis(e.ClientTS), meta,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert interaction event: %w", err)
	}
	return id, nil
}

// SceneEventWindow describes a slice of one (session, scene) event stream.
type SceneEventWindow struct {
	SessionID string
	SceneID   int
	// Before are up to Limit events strictly before From, newest first.
	Before []InteractionEvent
	// Within are events inside [From, To], oldest first.
	Within []InteractionEvent
	// After is the first event strictly after To, if any.
	After *InteractionEvent
}

// LoadSceneEventWindow fetches the events needed to replay a (session, scene)
// pair around a time window.
//
// The "before" events exist to recover playback state: a batch that contains
// only progress events says nothing about whether the video was playing, so the
// replay reaches back. If none of those carry a control event, one more query
// finds the most recent one, because without it the replay cannot tell playing
// from paused.
func (db *DB) LoadSceneEventWindow(ctx context.Context, sessionID string, sceneID int, from, to time.Time, beforeLimit int) (SceneEventWindow, error) {
	w := SceneEventWindow{SessionID: sessionID, SceneID: sceneID}

	const selectCols = `SELECT id, client_event_id, session_id, event_type, entity_type,
	                           entity_id, client_ts, metadata FROM interaction_events`

	before, err := db.queryEvents(ctx,
		selectCols+` WHERE session_id = ? AND entity_type = 'scene' AND entity_id = ?
		             AND client_ts < ? ORDER BY client_ts DESC, id DESC LIMIT ?`,
		sessionID, sceneID, ToMillis(from), beforeLimit)
	if err != nil {
		return w, err
	}

	hasControl := false
	for _, e := range before {
		if isControlEventType(e.EventType) {
			hasControl = true
			break
		}
	}
	if !hasControl {
		extra, err := db.queryEvents(ctx,
			selectCols+` WHERE session_id = ? AND entity_type = 'scene' AND entity_id = ?
			             AND event_type IN ('scene_watch_start','scene_watch_pause','scene_watch_complete','scene_seek')
			             AND client_ts < ? ORDER BY client_ts DESC, id DESC LIMIT 1`,
			sessionID, sceneID, ToMillis(from))
		if err != nil {
			return w, err
		}
		for _, e := range extra {
			duplicate := false
			for _, existing := range before {
				if existing.ID == e.ID {
					duplicate = true
					break
				}
			}
			if !duplicate {
				before = append(before, e)
			}
		}
		// Keep newest-first ordering after the append.
		sortEventsDesc(before)
	}
	w.Before = before

	if w.Within, err = db.queryEvents(ctx,
		selectCols+` WHERE session_id = ? AND entity_type = 'scene' AND entity_id = ?
		             AND client_ts >= ? AND client_ts <= ? ORDER BY client_ts ASC, id ASC`,
		sessionID, sceneID, ToMillis(from), ToMillis(to)); err != nil {
		return w, err
	}

	after, err := db.queryEvents(ctx,
		selectCols+` WHERE session_id = ? AND entity_type = 'scene' AND entity_id = ?
		             AND client_ts > ? ORDER BY client_ts ASC, id ASC LIMIT 1`,
		sessionID, sceneID, ToMillis(to))
	if err != nil {
		return w, err
	}
	if len(after) > 0 {
		w.After = &after[0]
	}
	return w, nil
}

// LatestSceneEventWithTypes returns the most recent event of the given types
// for a (session, scene) pair. Used to recover the video's duration, which the
// player attaches to several event types.
func (db *DB) LatestSceneEventWithTypes(ctx context.Context, sessionID string, sceneID int, types []string) (*InteractionEvent, error) {
	if len(types) == 0 {
		return nil, nil
	}

	args := []any{sessionID, sceneID}
	for _, t := range types {
		args = append(args, t)
	}

	events, err := db.queryEvents(ctx,
		`SELECT id, client_event_id, session_id, event_type, entity_type, entity_id, client_ts, metadata
		 FROM interaction_events
		 WHERE session_id = ? AND entity_type = 'scene' AND entity_id = ?
		   AND event_type IN (`+Placeholders(len(types))+`)
		 ORDER BY client_ts DESC, id DESC LIMIT 1`, args...)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, nil
	}
	return &events[0], nil
}

func (db *DB) queryEvents(ctx context.Context, query string, args ...any) ([]InteractionEvent, error) {
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query interaction events: %w", err)
	}
	defer rows.Close()

	var out []InteractionEvent
	for rows.Next() {
		var e InteractionEvent
		var clientEventID sql.NullString
		var clientTS int64
		var meta JSONText[map[string]any]

		if err := rows.Scan(&e.ID, &clientEventID, &e.SessionID, &e.EventType,
			&e.EntityType, &e.EntityID, &clientTS, &meta); err != nil {
			return nil, fmt.Errorf("scan interaction event: %w", err)
		}
		e.ClientEventID = StringPtr(clientEventID)
		e.ClientTS = FromMillis(clientTS)
		e.Metadata = meta.Data
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate interaction events: %w", err)
	}
	return out, nil
}

func isControlEventType(t string) bool {
	switch t {
	case "scene_watch_start", "scene_watch_pause", "scene_watch_complete", "scene_seek":
		return true
	default:
		return false
	}
}

func sortEventsDesc(events []InteractionEvent) {
	for i := 1; i < len(events); i++ {
		for j := i; j > 0; j-- {
			if events[j].ClientTS.After(events[j-1].ClientTS) {
				events[j], events[j-1] = events[j-1], events[j]
				continue
			}
			break
		}
	}
}

// -------------------------------------------------------------- sessions ---

// ErrSessionNotFound is returned when a session id has no row.
var ErrSessionNotFound = errors.New("interaction session not found")

// GetSession loads a session by id.
func (db *DB) GetSession(ctx context.Context, sessionID string) (InteractionSession, error) {
	return scanSession(db.sql.QueryRowContext(ctx, sessionSelect+` WHERE session_id = ?`, sessionID))
}

const sessionSelect = `SELECT id, session_id, last_event_ts, session_start_ts,
                              last_entity_type, last_entity_id, last_entity_event_ts,
                              client_fingerprint, ended_at
                       FROM interaction_sessions`

type rowScanner interface{ Scan(dest ...any) error }

func scanSession(row rowScanner) (InteractionSession, error) {
	var s InteractionSession
	var lastEventTS, sessionStartTS int64
	var lastEntityType, fingerprint sql.NullString
	var lastEntityID sql.NullInt64
	var lastEntityEventTS, endedAt NullTime

	err := row.Scan(&s.ID, &s.SessionID, &lastEventTS, &sessionStartTS,
		&lastEntityType, &lastEntityID, &lastEntityEventTS, &fingerprint, &endedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return InteractionSession{}, ErrSessionNotFound
	}
	if err != nil {
		return InteractionSession{}, fmt.Errorf("scan interaction session: %w", err)
	}

	s.LastEventTS = FromMillis(lastEventTS)
	s.SessionStartTS = FromMillis(sessionStartTS)
	s.LastEntityType = StringPtr(lastEntityType)
	if lastEntityID.Valid {
		v := int(lastEntityID.Int64)
		s.LastEntityID = &v
	}
	s.LastEntityEventTS = lastEntityEventTS.TimePtr()
	s.ClientFingerprint = StringPtr(fingerprint)
	s.EndedAt = endedAt.TimePtr()
	return s, nil
}

// CreateSession inserts a new session.
func (db *DB) CreateSession(ctx context.Context, s InteractionSession) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO interaction_sessions
		 (session_id, last_event_ts, session_start_ts, updated_at, client_fingerprint)
		 VALUES (?,?,?,?,?)`,
		s.SessionID, ToMillis(s.LastEventTS), ToMillis(s.SessionStartTS),
		NowMillis(), NullString(s.ClientFingerprint))
	if err != nil {
		return fmt.Errorf("create interaction session %q: %w", s.SessionID, err)
	}
	return nil
}

// UpdateSessionState persists the mutable parts of a session.
func UpdateSessionState(ctx context.Context, tx *sql.Tx, s InteractionSession) error {
	var lastEntityID any
	if s.LastEntityID != nil {
		lastEntityID = *s.LastEntityID
	}

	_, err := tx.ExecContext(ctx,
		`UPDATE interaction_sessions
		 SET last_event_ts = ?, last_entity_type = ?, last_entity_id = ?,
		     last_entity_event_ts = ?, updated_at = ?
		 WHERE id = ?`,
		ToMillis(s.LastEventTS), NullString(s.LastEntityType), lastEntityID,
		MillisPtr(s.LastEntityEventTS), NowMillis(), s.ID)
	if err != nil {
		return fmt.Errorf("update interaction session %q: %w", s.SessionID, err)
	}
	return nil
}

// ResolveSessionAlias returns the canonical id an alias points at.
func (db *DB) ResolveSessionAlias(ctx context.Context, alias string) (string, bool, error) {
	var canonical string
	err := db.sql.QueryRowContext(ctx,
		`SELECT canonical_session_id FROM interaction_session_aliases WHERE alias_session_id = ?`,
		alias).Scan(&canonical)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("resolve session alias: %w", err)
	}
	return canonical, true, nil
}

// AddSessionAlias records that an incoming session id maps onto a canonical
// one. A duplicate is not an error: two batches may race to merge the same id.
func (db *DB) AddSessionAlias(ctx context.Context, alias, canonical string) error {
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO interaction_session_aliases (alias_session_id, canonical_session_id, created_at)
		 VALUES (?,?,?)
		 ON CONFLICT (alias_session_id) DO NOTHING`,
		alias, canonical, NowMillis())
	if err != nil {
		return fmt.Errorf("add session alias: %w", err)
	}
	return nil
}

// FindRecentSessionForFingerprint returns the newest still-open session for a
// client whose last event is at or after the threshold. This is what stitches a
// page reload back onto the session it interrupted.
func (db *DB) FindRecentSessionForFingerprint(ctx context.Context, fingerprint string, threshold time.Time) (string, bool, error) {
	var sessionID string
	err := db.sql.QueryRowContext(ctx,
		`SELECT session_id FROM interaction_sessions
		 WHERE client_fingerprint = ? AND last_event_ts >= ? AND ended_at IS NULL
		 ORDER BY last_event_ts DESC LIMIT 1`,
		fingerprint, ToMillis(threshold)).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find recent session: %w", err)
	}
	return sessionID, true, nil
}

// StaleSessionsForFingerprint lists still-open sessions whose last event is
// older than the threshold.
func (db *DB) StaleSessionsForFingerprint(ctx context.Context, fingerprint string, threshold time.Time) ([]InteractionSession, error) {
	rows, err := db.sql.QueryContext(ctx,
		sessionSelect+` WHERE client_fingerprint = ? AND ended_at IS NULL AND last_event_ts < ?`,
		fingerprint, ToMillis(threshold))
	if err != nil {
		return nil, fmt.Errorf("list stale sessions: %w", err)
	}
	defer rows.Close()

	var out []InteractionSession
	for rows.Next() {
		s, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stale sessions: %w", err)
	}
	return out, nil
}

// FinalizeSessions marks sessions ended, at their own last event time rather
// than now: the session ended when the user stopped, not when we noticed.
func (db *DB) FinalizeSessions(ctx context.Context, ids []int64) error {
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
			`UPDATE interaction_sessions SET ended_at = last_event_ts, updated_at = `+
				fmt.Sprint(NowMillis())+` WHERE id IN (`+Placeholders(len(batch))+`)`,
			args...); err != nil {
			return fmt.Errorf("finalize sessions: %w", err)
		}
	}
	return nil
}

// UpdateSessions persists several sessions' mutable state in one transaction.
func (db *DB) UpdateSessions(ctx context.Context, sessions []InteractionSession) error {
	if len(sessions) == 0 {
		return nil
	}
	return db.InTx(ctx, func(tx *sql.Tx) error {
		for _, s := range sessions {
			if err := UpdateSessionState(ctx, tx, s); err != nil {
				return err
			}
		}
		return nil
	})
}
