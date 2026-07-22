package autotag

import (
	"context"
	"slices"

	"github.com/stashapp/stash/pkg/gallery"
	"github.com/stashapp/stash/pkg/image"
	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene"
)

// MatchResult holds the entity IDs matched against an object's path during the
// read-only collection phase of auto-tagging. It is produced by the Collect*
// functions (which perform no writes and are safe to run concurrently) and
// consumed by the Apply* functions (which must run within a write transaction).
//
// Splitting matching (read, CPU-bound, parallelisable) from application (write,
// serialised on stash's single write connection) lets the expensive path
// matching run across multiple goroutines while writes remain safely serial.
type MatchResult struct {
	PerformerIDs []int
	StudioID     *int
	TagIDs       []int
}

// Empty reports whether nothing matched, so the caller can skip the write phase.
func (r MatchResult) Empty() bool {
	return len(r.PerformerIDs) == 0 && r.StudioID == nil && len(r.TagIDs) == 0
}

// collectMatches runs the path-matching for a configured tagger. studioAlreadySet
// suppresses studio matching (studios are never overwritten), mirroring the
// behaviour of the SceneStudios/ImageStudios/GalleryStudios functions.
func collectMatches(ctx context.Context, t tagger, studioAlreadySet bool,
	performerReader models.PerformerAutoTagQueryer,
	studioReader models.StudioAutoTagQueryer,
	tagReader models.TagAutoTagQueryer,
	performers, studios, tags bool) (MatchResult, error) {

	var res MatchResult
	if t.Path == "" {
		return res, nil
	}

	if performers && performerReader != nil {
		matched, err := match.PathToPerformers(ctx, t.Path, performerReader, t.cache, t.trimExt)
		if err != nil {
			return res, err
		}
		for _, p := range matched {
			res.PerformerIDs = append(res.PerformerIDs, p.ID)
		}
	}

	if studios && !studioAlreadySet && studioReader != nil {
		studio, err := match.PathToStudio(ctx, t.Path, studioReader, t.cache, t.trimExt)
		if err != nil {
			return res, err
		}
		if studio != nil {
			id := studio.ID
			res.StudioID = &id
		}
	}

	if tags && tagReader != nil {
		matched, err := match.PathToTags(ctx, t.Path, tagReader, t.cache, t.trimExt)
		if err != nil {
			return res, err
		}
		for _, tg := range matched {
			res.TagIDs = append(res.TagIDs, tg.ID)
		}
	}

	return res, nil
}

// --- Scene ---

// SceneAutoTagUpdater is the write surface needed to apply scene match results.
type SceneAutoTagUpdater interface {
	models.PerformerIDLoader
	models.TagIDLoader
	models.SceneUpdater
}

// CollectSceneMatches performs the read-only path-matching phase for a scene.
// It performs no writes and is safe to run concurrently against a read
// transaction.
func CollectSceneMatches(ctx context.Context, s *models.Scene,
	performerReader models.PerformerAutoTagQueryer,
	studioReader models.StudioAutoTagQueryer,
	tagReader models.TagAutoTagQueryer,
	cache *match.Cache, performers, studios, tags bool) (MatchResult, error) {

	t := getSceneFileTagger(s, cache)
	return collectMatches(ctx, t, s.StudioID != nil, performerReader, studioReader, tagReader, performers, studios, tags)
}

// ApplySceneMatches writes the matched links for a scene, deduping against
// existing links. Must be called within a writable transaction.
func ApplySceneMatches(ctx context.Context, s *models.Scene, rw SceneAutoTagUpdater, res MatchResult) error {
	if len(res.PerformerIDs) > 0 {
		if err := s.LoadPerformerIDs(ctx, rw); err != nil {
			return err
		}
		existing := s.PerformerIDs.List()
		for _, id := range res.PerformerIDs {
			if slices.Contains(existing, id) {
				continue
			}
			if err := scene.AddPerformer(ctx, rw, s, id); err != nil {
				return err
			}
			existing = append(existing, id)
		}
	}

	if res.StudioID != nil {
		if _, err := addSceneStudio(ctx, rw, s, *res.StudioID); err != nil {
			return err
		}
	}

	if len(res.TagIDs) > 0 {
		if err := s.LoadTagIDs(ctx, rw); err != nil {
			return err
		}
		existing := s.TagIDs.List()
		for _, id := range res.TagIDs {
			if slices.Contains(existing, id) {
				continue
			}
			if err := scene.AddTag(ctx, rw, s, id); err != nil {
				return err
			}
			existing = append(existing, id)
		}
	}

	return nil
}

// --- Image ---

// ImageAutoTagUpdater is the write surface needed to apply image match results.
type ImageAutoTagUpdater interface {
	models.PerformerIDLoader
	models.TagIDLoader
	models.ImageUpdater
}

// CollectImageMatches performs the read-only path-matching phase for an image.
func CollectImageMatches(ctx context.Context, s *models.Image,
	performerReader models.PerformerAutoTagQueryer,
	studioReader models.StudioAutoTagQueryer,
	tagReader models.TagAutoTagQueryer,
	cache *match.Cache, performers, studios, tags bool) (MatchResult, error) {

	t := getImageFileTagger(s, cache)
	return collectMatches(ctx, t, s.StudioID != nil, performerReader, studioReader, tagReader, performers, studios, tags)
}

// ApplyImageMatches writes the matched links for an image, deduping against
// existing links. Must be called within a writable transaction.
func ApplyImageMatches(ctx context.Context, s *models.Image, rw ImageAutoTagUpdater, res MatchResult) error {
	if len(res.PerformerIDs) > 0 {
		if err := s.LoadPerformerIDs(ctx, rw); err != nil {
			return err
		}
		existing := s.PerformerIDs.List()
		for _, id := range res.PerformerIDs {
			if slices.Contains(existing, id) {
				continue
			}
			if err := image.AddPerformer(ctx, rw, s, id); err != nil {
				return err
			}
			existing = append(existing, id)
		}
	}

	if res.StudioID != nil {
		if _, err := addImageStudio(ctx, rw, s, *res.StudioID); err != nil {
			return err
		}
	}

	if len(res.TagIDs) > 0 {
		if err := s.LoadTagIDs(ctx, rw); err != nil {
			return err
		}
		existing := s.TagIDs.List()
		for _, id := range res.TagIDs {
			if slices.Contains(existing, id) {
				continue
			}
			if err := image.AddTag(ctx, rw, s, id); err != nil {
				return err
			}
			existing = append(existing, id)
		}
	}

	return nil
}

// --- Gallery ---

// GalleryAutoTagUpdater is the write surface needed to apply gallery match results.
type GalleryAutoTagUpdater interface {
	models.PerformerIDLoader
	models.TagIDLoader
	models.GalleryQueryer
	models.GalleryUpdater
}

// CollectGalleryMatches performs the read-only path-matching phase for a gallery.
func CollectGalleryMatches(ctx context.Context, s *models.Gallery,
	performerReader models.PerformerAutoTagQueryer,
	studioReader models.StudioAutoTagQueryer,
	tagReader models.TagAutoTagQueryer,
	cache *match.Cache, performers, studios, tags bool) (MatchResult, error) {

	t := getGalleryFileTagger(s, cache)
	return collectMatches(ctx, t, s.StudioID != nil, performerReader, studioReader, tagReader, performers, studios, tags)
}

// ApplyGalleryMatches writes the matched links for a gallery, deduping against
// existing links. Must be called within a writable transaction.
func ApplyGalleryMatches(ctx context.Context, s *models.Gallery, rw GalleryAutoTagUpdater, res MatchResult) error {
	if len(res.PerformerIDs) > 0 {
		if err := s.LoadPerformerIDs(ctx, rw); err != nil {
			return err
		}
		existing := s.PerformerIDs.List()
		for _, id := range res.PerformerIDs {
			if slices.Contains(existing, id) {
				continue
			}
			if err := gallery.AddPerformer(ctx, rw, s, id); err != nil {
				return err
			}
			existing = append(existing, id)
		}
	}

	if res.StudioID != nil {
		if _, err := addGalleryStudio(ctx, rw, s, *res.StudioID); err != nil {
			return err
		}
	}

	if len(res.TagIDs) > 0 {
		if err := s.LoadTagIDs(ctx, rw); err != nil {
			return err
		}
		existing := s.TagIDs.List()
		for _, id := range res.TagIDs {
			if slices.Contains(existing, id) {
				continue
			}
			if err := gallery.AddTag(ctx, rw, s, id); err != nil {
				return err
			}
			existing = append(existing, id)
		}
	}

	return nil
}
