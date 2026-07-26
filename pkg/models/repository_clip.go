package models

import "context"

// ClipGetter provides methods to get clips by ID.
type ClipGetter interface {
	FindMany(ctx context.Context, ids []int) ([]*Clip, error)
	Find(ctx context.Context, id int) (*Clip, error)
}

// ClipFinder provides methods to find clips.
type ClipFinder interface {
	ClipGetter
	FindBySceneID(ctx context.Context, sceneID int) ([]*Clip, error)
}

// ClipQueryer provides methods to query clips.
type ClipQueryer interface {
	Query(ctx context.Context, clipFilter *ClipFilterType, findFilter *FindFilterType) ([]*Clip, int, error)
	QueryCount(ctx context.Context, clipFilter *ClipFilterType, findFilter *FindFilterType) (int, error)
}

// ClipCounter provides methods to count clips.
type ClipCounter interface {
	Count(ctx context.Context) (int, error)
}

// ClipCreator provides methods to create clips.
type ClipCreator interface {
	Create(ctx context.Context, newClip *Clip) error
}

// ClipUpdater provides methods to update clips.
type ClipUpdater interface {
	Update(ctx context.Context, updatedClip *Clip) error
	UpdatePartial(ctx context.Context, id int, updatedClip ClipPartial) (*Clip, error)
}

// ClipDestroyer provides methods to destroy clips.
type ClipDestroyer interface {
	Destroy(ctx context.Context, id int) error
}

// ClipReader provides all methods to read clips.
type ClipReader interface {
	ClipFinder
	ClipQueryer
	ClipCounter
	TagIDLoader
	ViewDateReader
	ODateReader

	All(ctx context.Context) ([]*Clip, error)
}

// ClipWriter provides all methods to modify clips.
type ClipWriter interface {
	ClipCreator
	ClipUpdater
	ClipDestroyer
	OHistoryWriter
	ViewHistoryWriter
}

// ClipReaderWriter provides all clip methods.
type ClipReaderWriter interface {
	ClipReader
	ClipWriter
}
