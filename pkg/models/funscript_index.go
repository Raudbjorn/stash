package models

import "context"

// FunscriptIndex is a single indexed local funscript file. It backs the native
// marker-sync funscript matching/submission feature (migration 95) and mirrors
// the community timestamp.trade plugin's former `script_index` table. It is not
// a first-class GraphQL-exposed domain entity; only marker sync consumes it.
type FunscriptIndex struct {
	// ID is the autoincrement primary key.
	ID int
	// Filename is the absolute path of the indexed .funscript file.
	Filename string
	// Metadata is the funscript's top-level "metadata" object as raw JSON text,
	// or "" when the file has none.
	Metadata string
	// SceneID is the id of the scene this funscript was matched to (by filename
	// stem), or nil when unmatched.
	SceneID *int
	// MD5 is the md5 checksum of the funscript file's bytes (content address used
	// to match a source-provided funscript hash to a local file).
	MD5 string
}

// FunscriptIndexReader reads the local funscript index.
type FunscriptIndexReader interface {
	// ExistsByFilename reports whether a row with the given (absolute) filename
	// already exists. Used to make (re-)indexing idempotent.
	ExistsByFilename(ctx context.Context, filename string) (bool, error)
	// FindByMD5 returns every indexed funscript with the given md5.
	FindByMD5(ctx context.Context, md5 string) ([]FunscriptIndex, error)
	// FindBySceneID returns every indexed funscript matched to the given scene.
	FindBySceneID(ctx context.Context, sceneID int) ([]FunscriptIndex, error)
	// UnmatchedRows returns every indexed funscript not yet matched to a scene.
	UnmatchedRows(ctx context.Context) ([]FunscriptIndex, error)
}

// FunscriptIndexWriter writes the local funscript index.
type FunscriptIndexWriter interface {
	// Create inserts f and back-fills f.ID.
	Create(ctx context.Context, f *FunscriptIndex) error
	// SetSceneID sets the matched scene id on the row with the given id.
	SetSceneID(ctx context.Context, id int, sceneID int) error
}

// FunscriptIndexReaderWriter is the combined read/write interface bound into
// models.Repository.
type FunscriptIndexReaderWriter interface {
	FunscriptIndexReader
	FunscriptIndexWriter
}
