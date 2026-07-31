package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// RecommendationPreference records which recommender the user chose for a given
// UI context, along with that recommender's configuration.
//
// Context is unique: one preference per context, overwritten on save.
type RecommendationPreference struct {
	ID            int64
	Context       string
	RecommenderID string
	Config        map[string]any
}

// ErrNoPreference is returned when no preference has been saved for a context.
var ErrNoPreference = errors.New("no recommendation preference for context")

// GetRecommendationPreference reads the preference for a context.
func (db *DB) GetRecommendationPreference(ctx context.Context, uiContext string) (RecommendationPreference, error) {
	if uiContext == "" {
		return RecommendationPreference{}, ErrNoPreference
	}

	var p RecommendationPreference
	var config JSONText[map[string]any]
	err := db.sql.QueryRowContext(ctx,
		`SELECT id, context, recommender_id, config
		 FROM recommendation_preferences WHERE context = ?`, uiContext).
		Scan(&p.ID, &p.Context, &p.RecommenderID, &config)
	if errors.Is(err, sql.ErrNoRows) {
		return RecommendationPreference{}, ErrNoPreference
	}
	if err != nil {
		return RecommendationPreference{}, fmt.Errorf("get recommendation preference %q: %w", uiContext, err)
	}

	p.Config = config.Data
	if p.Config == nil {
		p.Config = map[string]any{}
	}
	return p, nil
}

// SaveRecommendationPreference stores the preference for a context, replacing
// any existing one. A nil config is stored as an empty object, matching the
// Python original's `config or {}`.
func (db *DB) SaveRecommendationPreference(ctx context.Context, uiContext, recommenderID string, config map[string]any) (RecommendationPreference, error) {
	if uiContext == "" {
		return RecommendationPreference{}, errors.New("recommendation preference requires a context")
	}
	if config == nil {
		config = map[string]any{}
	}

	arg, err := MarshalArg(config)
	if err != nil {
		return RecommendationPreference{}, fmt.Errorf("encode recommendation config: %w", err)
	}

	now := NowMillis()
	// The unique constraint on context makes this an upsert rather than a
	// read-modify-write, which also removes a race between concurrent saves.
	_, err = db.sql.ExecContext(ctx,
		`INSERT INTO recommendation_preferences (context, recommender_id, config, created_at, updated_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT (context) DO UPDATE SET
		     recommender_id = excluded.recommender_id,
		     config         = excluded.config,
		     updated_at     = excluded.updated_at`,
		uiContext, recommenderID, arg, now, now)
	if err != nil {
		return RecommendationPreference{}, fmt.Errorf("save recommendation preference %q: %w", uiContext, err)
	}

	return db.GetRecommendationPreference(ctx, uiContext)
}

// ListRecommendationPreferences returns every saved preference, ordered by
// context for stable output.
func (db *DB) ListRecommendationPreferences(ctx context.Context) ([]RecommendationPreference, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, context, recommender_id, config
		 FROM recommendation_preferences ORDER BY context`)
	if err != nil {
		return nil, fmt.Errorf("list recommendation preferences: %w", err)
	}
	defer rows.Close()

	var out []RecommendationPreference
	for rows.Next() {
		var p RecommendationPreference
		var config JSONText[map[string]any]
		if err := rows.Scan(&p.ID, &p.Context, &p.RecommenderID, &config); err != nil {
			return nil, fmt.Errorf("scan recommendation preference: %w", err)
		}
		p.Config = config.Data
		if p.Config == nil {
			p.Config = map[string]any{}
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recommendation preferences: %w", err)
	}
	return out, nil
}
