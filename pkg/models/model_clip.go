package models

import (
	"context"
	"time"
)

// Clip represents a frame-accurate time-range within a parent Scene.
type Clip struct {
	ID      int      `json:"id"`
	Title   string   `json:"title"`
	SceneID int      `json:"scene_id"`
	Seconds float64  `json:"seconds"`
	// EndSeconds is the optional end time of the clip in seconds.
	EndSeconds *float64 `json:"end_seconds"`
	// Rating expressed in 1-100 scale
	Rating    *int      `json:"rating"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// transient - not persisted directly on the clip row
	TagIDs RelatedIDs `json:"tag_ids"`
}

func NewClip() Clip {
	currentTime := time.Now()
	return Clip{
		CreatedAt: currentTime,
		UpdatedAt: currentTime,
	}
}

func (m *Clip) LoadTagIDs(ctx context.Context, l TagIDLoader) error {
	return m.TagIDs.load(func() ([]int, error) {
		return l.GetTagIDs(ctx, m.ID)
	})
}

// ClipPartial represents part of a Clip object.
// It is used to update the database entry.
type ClipPartial struct {
	Title      OptionalString
	SceneID    OptionalInt
	Seconds    OptionalFloat64
	EndSeconds OptionalFloat64
	// Rating expressed in 1-100 scale
	Rating    OptionalInt
	TagIDs    *UpdateIDs
	CreatedAt OptionalTime
	UpdatedAt OptionalTime
}

func NewClipPartial() ClipPartial {
	currentTime := time.Now()
	return ClipPartial{
		UpdatedAt: NewOptionalTime(currentTime),
	}
}
