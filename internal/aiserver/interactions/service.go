package interactions

import (
	"context"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// EventIn is one event as posted by the frontend.
//
// The field names are the wire contract: `id` is the client's own event id
// (used for deduplication), and `type` names the event. Metadata is free-form
// because different event types carry different payloads.
type EventIn struct {
	ID         string         `json:"id"`
	SessionID  string         `json:"session_id"`
	ClientID   *string        `json:"client_id"`
	TS         time.Time      `json:"ts"`
	Type       string         `json:"type"`
	EntityType string         `json:"entity_type"`
	EntityID   any            `json:"entity_id"`
	Metadata   map[string]any `json:"metadata"`
}

// IngestResult summarises a batch.
type IngestResult struct {
	Accepted   int      `json:"accepted"`
	Duplicates int      `json:"duplicates"`
	Errors     []string `json:"errors"`
}

// Settings are the tunables read from the AI database's system settings.
//
// They are resolved once per batch rather than per event: they change rarely
// and reading them per event would dominate the ingest cost.
type Settings struct {
	// MinSessionMinutes is how long a session must last before it credits a
	// derived count to the entity it ended on.
	MinSessionMinutes float64
	// MergeTTL is how close together two sessions from the same client must be
	// to be treated as one.
	MergeTTL time.Duration
	// SegmentMergeGap coalesces watched spans within this many seconds.
	SegmentMergeGap float64
	// SegmentTimeMargin widens the replay window at each end.
	SegmentTimeMargin time.Duration
	// SegmentMinDuration discards watched spans shorter than this.
	SegmentMinDuration float64
}

// Setting keys, matching the rows seeded into the AI database.
const (
	settingMinSessionMinutes  = "INTERACTION_MIN_SESSION_MINUTES"
	settingMergeTTLSeconds    = "INTERACTION_MERGE_TTL_SECONDS"
	settingSegmentMergeGap    = "SEGMENT_MERGE_GAP_SECONDS"
	settingSegmentTimeMargin  = "INTERACTION_SEGMENT_TIME_MARGIN_SECONDS"
	settingSegmentMinDuration = "SEGMENT_MIN_DURATION_SECONDS"
)

// Defaults, matching the seeded values.
const (
	defaultMinSessionMinutes  = 10.0
	defaultMergeTTLSeconds    = 120.0
	defaultSegmentMergeGap    = 0.5
	defaultSegmentTimeMargin  = 2.0
	defaultSegmentMinDuration = 1.5
	// beforeEventLimit is how far back the replay reaches for playback state.
	beforeEventLimit = 5
)

// LoadSettings reads the tunables from the database.
func LoadSettings(ctx context.Context, db *store.DB) Settings {
	return Settings{
		MinSessionMinutes:  db.SettingFloat(ctx, settingMinSessionMinutes, defaultMinSessionMinutes),
		MergeTTL:           secondsToDuration(db.SettingFloat(ctx, settingMergeTTLSeconds, defaultMergeTTLSeconds)),
		SegmentMergeGap:    db.SettingFloat(ctx, settingSegmentMergeGap, defaultSegmentMergeGap),
		SegmentTimeMargin:  secondsToDuration(db.SettingFloat(ctx, settingSegmentTimeMargin, defaultSegmentTimeMargin)),
		SegmentMinDuration: db.SettingFloat(ctx, settingSegmentMinDuration, defaultSegmentMinDuration),
	}
}

func secondsToDuration(s float64) time.Duration {
	return time.Duration(s * float64(time.Second))
}

// Service ingests interaction events.
type Service struct {
	db *store.DB
	// Now is the clock, injectable for tests.
	Now func() time.Time
}

// NewService builds an ingest service.
func NewService(db *store.DB) *Service {
	return &Service{db: db, Now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// normalizedEvent is an event after validation, ready to store.
type normalizedEvent struct {
	in EventIn
	// sessionID is the canonical session, which may differ from what the
	// client sent if its session was merged into an existing one.
	sessionID string
	entityID  int
	clientTS  time.Time
	metadata  map[string]any
	// store is false for progress events: they drive state but are never
	// persisted, which is what keeps the raw event table small.
	store bool
}
