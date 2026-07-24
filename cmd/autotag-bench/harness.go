package main

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"time"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/sqlite"
	"github.com/stashapp/stash/pkg/txn"
)

const benchRoot = "/bench"

// setupResult carries the opened DB and generated entities through to the
// measurement phase.
type setupResult struct {
	db         *sqlite.Database
	repo       models.Repository
	performers []synthEntity
	studios    []synthEntity
	tags       []synthEntity
	paths      []pathSpec
	dbPath     string
	setupTook  time.Duration
}

// setup creates a temporary sqlite DB at the latest schema version and
// populates it with `p` synthetic performers/studios/tags plus `p.files`
// scenes/images/galleries whose paths embed entity tokens.
func setup(ctx context.Context, p preset, rng *rand.Rand) (*setupResult, error) {
	_ = config.InitializeEmpty()

	f, err := os.CreateTemp("", fmt.Sprintf("autotag-bench-%s-*.sqlite", p.name))
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	dbPath := f.Name()
	f.Close()

	db := sqlite.NewDatabase()
	db.SetBlobStoreOptions(sqlite.BlobStoreOptions{UseDatabase: true})
	if err := db.Open(dbPath); err != nil {
		return nil, fmt.Errorf("opening db: %w", err)
	}

	res := &setupResult{db: db, repo: db.Repository(), dbPath: dbPath}
	res.performers = generatePerformers(p.performers, rng)
	res.studios = generateStudios(p.studios, rng)
	res.tags = generateTags(p.tags, rng)
	res.paths = generatePaths(p.files, res.performers, res.studios, res.tags, rng)

	begin := time.Now()
	if err := populateDB(ctx, res); err != nil {
		return nil, fmt.Errorf("populating db: %w", err)
	}
	res.setupTook = time.Since(begin)
	return res, nil
}

// cleanup closes the DB and removes the sqlite file.
func (s *setupResult) cleanup() {
	if s.db != nil {
		_ = s.db.Close()
	}
	if s.dbPath != "" {
		_ = os.Remove(s.dbPath)
		_ = os.Remove(s.dbPath + "-wal")
		_ = os.Remove(s.dbPath + "-shm")
	}
}

func populateDB(ctx context.Context, s *setupResult) error {
	r := s.repo
	return txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		if err := insertPerformers(ctx, r, s.performers); err != nil {
			return fmt.Errorf("performers: %w", err)
		}
		if err := insertStudios(ctx, r, s.studios); err != nil {
			return fmt.Errorf("studios: %w", err)
		}
		if err := insertTags(ctx, r, s.tags); err != nil {
			return fmt.Errorf("tags: %w", err)
		}
		folderID, err := insertRootFolder(ctx, r)
		if err != nil {
			return fmt.Errorf("root folder: %w", err)
		}
		if err := insertFiles(ctx, r, folderID, s.paths); err != nil {
			return fmt.Errorf("files: %w", err)
		}
		return nil
	})
}

func insertPerformers(ctx context.Context, r models.Repository, ps []synthEntity) error {
	now := time.Now()
	for i := range ps {
		p := &models.Performer{
			Name:      ps[i].name,
			Favorite:  false,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: p}); err != nil {
			return err
		}
	}
	return nil
}

func insertStudios(ctx context.Context, r models.Repository, ss []synthEntity) error {
	now := time.Now()
	for i := range ss {
		st := &models.Studio{
			Name:      ss[i].name,
			CreatedAt: now,
			UpdatedAt: now,
			Aliases:   models.NewRelatedStrings(ss[i].aliases),
		}
		if err := r.Studio.Create(ctx, &models.CreateStudioInput{Studio: st}); err != nil {
			return err
		}
	}
	return nil
}

func insertTags(ctx context.Context, r models.Repository, ts []synthEntity) error {
	now := time.Now()
	for i := range ts {
		t := &models.Tag{
			Name:      ts[i].name,
			CreatedAt: now,
			UpdatedAt: now,
			Aliases:   models.NewRelatedStrings(ts[i].aliases),
		}
		if err := r.Tag.Create(ctx, &models.CreateTagInput{Tag: t}); err != nil {
			return err
		}
	}
	return nil
}

func insertRootFolder(ctx context.Context, r models.Repository) (models.FolderID, error) {
	now := time.Now()
	f := &models.Folder{
		Path:      benchRoot,
		DirEntry:  models.DirEntry{ModTime: now},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := r.Folder.Create(ctx, f); err != nil {
		return 0, err
	}
	return f.ID, nil
}

func insertFiles(ctx context.Context, r models.Repository, folderID models.FolderID, paths []pathSpec) error {
	now := time.Now()
	for i, p := range paths {
		base := &models.BaseFile{
			Basename:       p.basename,
			ParentFolderID: folderID,
			DirEntry:       models.DirEntry{ModTime: now},
			Size:           int64(i + 1),
			CreatedAt:      now,
			UpdatedAt:      now,
		}

		var file models.File
		switch p.kind {
		case kindScene:
			file = &models.VideoFile{
				BaseFile: base,
				Format:   "mp4", Width: 1920, Height: 1080, Duration: 60, VideoCodec: "h264", AudioCodec: "aac", FrameRate: 30, BitRate: 4000000,
			}
		case kindImage:
			file = &models.ImageFile{
				BaseFile: base,
				Format:   "jpg", Width: 1920, Height: 1080,
			}
		case kindGallery:
			file = base
		}

		if err := r.File.Create(ctx, file); err != nil {
			return fmt.Errorf("file %d: %w", i, err)
		}

		switch p.kind {
		case kindScene:
			sc := &models.Scene{CreatedAt: now, UpdatedAt: now}
			if err := r.Scene.Create(ctx, sc, []models.FileID{file.Base().ID}); err != nil {
				return fmt.Errorf("scene %d: %w", i, err)
			}
		case kindImage:
			im := &models.Image{CreatedAt: now, UpdatedAt: now}
			if err := r.Image.Create(ctx, &models.CreateImageInput{Image: im, FileIDs: []models.FileID{file.Base().ID}}); err != nil {
				return fmt.Errorf("image %d: %w", i, err)
			}
		case kindGallery:
			g := &models.Gallery{CreatedAt: now, UpdatedAt: now}
			if err := r.Gallery.Create(ctx, &models.CreateGalleryInput{Gallery: g, FileIDs: []models.FileID{file.Base().ID}}); err != nil {
				return fmt.Errorf("gallery %d: %w", i, err)
			}
		}
	}
	return nil
}

// runAutoTag invokes the file-based auto-tag flow via the manager's bench
// entry point. Uses a fresh Progress so metrics aren't polluted by setup
// activity.
func runAutoTag(ctx context.Context, r models.Repository) {
	progress := &job.Progress{}
	manager.RunAutoTagFilesForBench(ctx, r, progress, []string{benchRoot}, true, true, true)
}

