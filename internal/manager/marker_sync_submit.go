package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/markersync"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

// markerSyncSkipSubmitTagName is the tag name that, when attached to a scene,
// excludes that scene from marker sync submission. It mirrors the community
// timestamp.trade plugin's skip tag. The tag is never created by this package;
// a missing skip tag simply means nothing is skipped.
const markerSyncSkipSubmitTagName = "[Timestamp: Skip Submit]"

// MarkerSyncSubmitInput is the GraphQL input for the markerSyncSubmit mutation.
// It is autobound by gqlgen (see gqlgen.yml).
type MarkerSyncSubmitInput struct {
	// SceneIDs restricts submission to the given scene ids. When empty every
	// scene in the library is considered.
	SceneIDs []string `json:"scene_ids"`
}

// toStashIDRefs maps model stash ids to the neutral markersync representation.
func toStashIDRefs(ids []models.StashID) []markersync.StashIDRef {
	out := make([]markersync.StashIDRef, 0, len(ids))
	for _, s := range ids {
		out = append(out, markersync.StashIDRef{Endpoint: s.Endpoint, StashID: s.StashID})
	}
	return out
}

// buildSceneSubmission loads the full detail of a scene and maps it into the
// neutral markersync.SceneSubmission. It must be called inside a read
// transaction (it issues repository reads). Relationship loaders short-circuit
// when a relationship is already loaded, so pre-loading in the caller is safe.
func buildSceneSubmission(ctx context.Context, r models.Repository, s *models.Scene, submitFunscriptHashes bool) (markersync.SceneSubmission, error) {
	sr := r.Scene

	if err := s.LoadStashIDs(ctx, sr); err != nil {
		return markersync.SceneSubmission{}, fmt.Errorf("loading stash ids for scene %d: %w", s.ID, err)
	}
	if err := s.LoadURLs(ctx, sr); err != nil {
		return markersync.SceneSubmission{}, fmt.Errorf("loading urls for scene %d: %w", s.ID, err)
	}
	if err := s.LoadFiles(ctx, sr); err != nil {
		return markersync.SceneSubmission{}, fmt.Errorf("loading files for scene %d: %w", s.ID, err)
	}
	if err := s.LoadPerformerIDs(ctx, sr); err != nil {
		return markersync.SceneSubmission{}, fmt.Errorf("loading performer ids for scene %d: %w", s.ID, err)
	}
	if err := s.LoadTagIDs(ctx, sr); err != nil {
		return markersync.SceneSubmission{}, fmt.Errorf("loading tag ids for scene %d: %w", s.ID, err)
	}

	sub := markersync.SceneSubmission{
		Title:    s.Title,
		Details:  s.Details,
		URLs:     s.URLs.List(),
		StashIDs: toStashIDRefs(s.StashIDs.List()),
	}
	if s.Date != nil {
		sub.Date = s.Date.String()
	}

	// Markers: native seconds preserved; resolve the primary tag name.
	markers, err := r.SceneMarker.FindBySceneID(ctx, s.ID)
	if err != nil {
		return markersync.SceneSubmission{}, fmt.Errorf("loading markers for scene %d: %w", s.ID, err)
	}
	sub.Markers = make([]markersync.SubmissionMarker, 0, len(markers))
	for _, m := range markers {
		var tagName string
		tag, err := r.Tag.Find(ctx, m.PrimaryTagID)
		if err != nil {
			return markersync.SceneSubmission{}, fmt.Errorf("resolving primary tag %d: %w", m.PrimaryTagID, err)
		}
		if tag != nil {
			tagName = tag.Name
		}
		sub.Markers = append(sub.Markers, markersync.SubmissionMarker{
			Title:      m.Title,
			Seconds:    m.Seconds, // stash-native seconds, unchanged
			PrimaryTag: tagName,
		})
	}

	// Performers: name + stash ids.
	if perfIDs := s.PerformerIDs.List(); len(perfIDs) > 0 {
		performers, err := r.Performer.FindMany(ctx, perfIDs)
		if err != nil {
			return markersync.SceneSubmission{}, fmt.Errorf("loading performers for scene %d: %w", s.ID, err)
		}
		sub.Performers = make([]markersync.NamedEntity, 0, len(performers))
		for _, p := range performers {
			if p == nil {
				continue
			}
			if err := p.LoadStashIDs(ctx, r.Performer); err != nil {
				return markersync.SceneSubmission{}, fmt.Errorf("loading stash ids for performer %d: %w", p.ID, err)
			}
			sub.Performers = append(sub.Performers, markersync.NamedEntity{
				Name:     p.Name,
				StashIDs: toStashIDRefs(p.StashIDs.List()),
			})
		}
	}

	// Tags: scene tag names.
	if tagIDs := s.TagIDs.List(); len(tagIDs) > 0 {
		tags, err := r.Tag.FindMany(ctx, tagIDs)
		if err != nil {
			return markersync.SceneSubmission{}, fmt.Errorf("loading tags for scene %d: %w", s.ID, err)
		}
		sub.Tags = make([]string, 0, len(tags))
		for _, t := range tags {
			if t == nil {
				continue
			}
			sub.Tags = append(sub.Tags, t.Name)
		}
	}

	// Studio: name + stash ids.
	if s.StudioID != nil {
		st, err := r.Studio.Find(ctx, *s.StudioID)
		if err != nil {
			return markersync.SceneSubmission{}, fmt.Errorf("loading studio %d: %w", *s.StudioID, err)
		}
		if st != nil {
			if err := st.LoadStashIDs(ctx, r.Studio); err != nil {
				return markersync.SceneSubmission{}, fmt.Errorf("loading stash ids for studio %d: %w", st.ID, err)
			}
			sub.Studio = &markersync.NamedEntity{
				Name:     st.Name,
				StashIDs: toStashIDRefs(st.StashIDs.List()),
			}
		}
	}

	// Files: basename, duration, size, fingerprints.
	files := s.Files.List()
	sub.Files = make([]markersync.SubmissionFile, 0, len(files))
	for _, f := range files {
		if f == nil {
			continue
		}
		bf := f.Base()
		basename := bf.Basename
		if basename == "" {
			basename = filepath.Base(bf.Path)
		}
		fps := make([]markersync.Fingerprint, 0, len(bf.Fingerprints))
		for _, fp := range bf.Fingerprints {
			fps = append(fps, markersync.Fingerprint{Type: fp.Type, Value: fp.Value()})
		}
		sub.Files = append(sub.Files, markersync.SubmissionFile{
			Basename:     basename,
			Duration:     f.Duration,
			Size:         bf.Size,
			Fingerprints: fps,
		})
	}

	// Funscript hashes: attach the scene's indexed funscripts (basename, raw
	// metadata JSON, md5) so the source can match funscripts across users. Gated
	// by config; a scene with no indexed funscripts yields an empty slice, which
	// the adapter omits from the wire payload.
	if submitFunscriptHashes {
		rows, err := r.FunscriptIndex.FindBySceneID(ctx, s.ID)
		if err != nil {
			return markersync.SceneSubmission{}, fmt.Errorf("loading funscript hashes for scene %d: %w", s.ID, err)
		}
		sub.FunscriptHashes = make([]markersync.FunscriptHash, 0, len(rows))
		for _, row := range rows {
			// Guard against a corrupt stored metadata blob: invalid JSON would
			// otherwise abort the whole scene submission when the payload is
			// marshalled. Omit just this funscript's metadata (send null) and log.
			meta := row.Metadata
			if meta != "" && !json.Valid([]byte(meta)) {
				logger.Warnf("Marker Sync Submit: scene %d: funscript %q has invalid metadata JSON; omitting its metadata", s.ID, row.Filename)
				meta = ""
			}
			sub.FunscriptHashes = append(sub.FunscriptHashes, markersync.FunscriptHash{
				Filename: filepath.Base(row.Filename),
				Metadata: meta,
				MD5:      row.MD5,
			})
		}
	}

	return sub, nil
}

// buildMarkerSyncSubmitters constructs the marker sync submitters from the
// persisted marker sync configuration. timestamp.trade is the only
// bidirectional source (ThePornDB is fetch-only) and is constructed only when
// enabled in the config.
func buildMarkerSyncSubmitters() []markersync.Submitter {
	cfg := config.GetInstance().GetMarkerSyncConfig()

	var submitters []markersync.Submitter
	if cfg.TimestampTrade.Enabled {
		submitters = append(submitters, markersync.NewTimestampTradeSource(markersync.TimestampTradeOptions{
			Enabled:           true,
			BaseURL:           cfg.TimestampTrade.BaseURL,
			RequestsPerMinute: cfg.TimestampTrade.RequestsPerMinute,
		}))
	}
	return submitters
}

// markerSyncSubmitTask submits a single scene to the configured submitters.
type markerSyncSubmitTask struct {
	scene      *models.Scene
	submitters []markersync.Submitter
	// submitFunscriptHashes gates attaching the scene's indexed funscript hashes
	// to the submission payload.
	submitFunscriptHashes bool
}

// GetDescription implements Task.
func (t *markerSyncSubmitTask) GetDescription() string {
	return fmt.Sprintf("Submitting markers for scene %d", t.scene.ID)
}

// Start implements Task. The submission is built inside a read transaction; the
// network POST happens outside any transaction (same discipline as fetch). A
// submit failure for one submitter never aborts the others or the batch.
func (t *markerSyncSubmitTask) Start(ctx context.Context) {
	var sub markersync.SceneSubmission
	r := instance.Repository
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		sub, err = buildSceneSubmission(ctx, r, t.scene, t.submitFunscriptHashes)
		return err
	}); err != nil {
		logger.Errorf("Marker Sync Submit: scene %d: building submission: %v", t.scene.ID, err)
		return
	}

	for _, sm := range t.submitters {
		if !sm.Enabled() {
			continue
		}
		if err := sm.SubmitScene(ctx, sub); err != nil {
			logger.Warnf("Marker Sync Submit: scene %d: submitter %s: %v", t.scene.ID, sm.Name(), err)
			continue
		}
		logger.Infof("Marker Sync Submit: scene %d: submitted to %s", t.scene.ID, sm.Name())
	}
}

// MarkerSyncSubmit launches a background job that submits scene markers (and the
// enclosing scene metadata) to the configured external databases. It returns
// the job id.
func (s *Manager) MarkerSyncSubmit(ctx context.Context, input MarkerSyncSubmitInput) int {
	j := job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
		logger.Infof("Initiating marker sync submit")

		cfg := config.GetInstance().GetMarkerSyncConfig()
		submitters := buildMarkerSyncSubmitters()

		scenes, err := s.markerSyncSubmitScenes(ctx, input)
		if err != nil {
			return err
		}

		if len(scenes) == 0 {
			logger.Infof("Marker Sync Submit: no matching scenes")
			return nil
		}

		progress.SetTotal(len(scenes))
		logger.Infof("Marker Sync Submit: starting for %d scenes", len(scenes))

		for _, sc := range scenes {
			if job.IsCancelled(ctx) {
				logger.Info("Marker Sync Submit: stopping due to user request")
				return nil
			}

			task := &markerSyncSubmitTask{
				scene:                 sc,
				submitters:            submitters,
				submitFunscriptHashes: cfg.SubmitFunscriptHash,
			}
			progress.ExecuteTask(task.GetDescription(), func() {
				task.Start(ctx)
			})
			progress.Increment()
		}

		logger.Info("Marker Sync Submit: finished")
		return nil
	})

	return s.JobManager.Add(ctx, "Marker Sync Submit", j)
}

// markerSyncSubmitScenes resolves the scene set for a submit run. Scenes with no
// markers (nothing to submit) and scenes bearing the skip-submit tag are
// excluded. Resolution runs inside a read transaction.
func (s *Manager) markerSyncSubmitScenes(ctx context.Context, input MarkerSyncSubmitInput) ([]*models.Scene, error) {
	var scenes []*models.Scene
	err := s.Repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var candidates []*models.Scene

		if len(input.SceneIDs) > 0 {
			ids, err := stringslice.StringSliceToIntSlice(input.SceneIDs)
			if err != nil {
				return fmt.Errorf("converting scene ids: %w", err)
			}
			found, err := s.SceneService.FindByIDs(ctx, ids, scene.LoadStashIDs)
			if err != nil {
				return fmt.Errorf("finding scenes by id: %w", err)
			}
			candidates = found
		} else {
			// NOTE: loads the whole library into memory. Adequate for a
			// background job on typical libraries; a paged query is a follow-up
			// for very large libraries.
			all, err := s.Repository.Scene.All(ctx)
			if err != nil {
				return fmt.Errorf("loading all scenes: %w", err)
			}
			candidates = all
		}

		// Resolve the skip-submit tag once; id 0 means it does not exist and no
		// scene is skipped for it.
		skipTagID, err := s.markerSyncSkipSubmitTagID(ctx)
		if err != nil {
			return err
		}

		for _, sc := range candidates {
			// Honour job cancellation while scanning a large library.
			if err := ctx.Err(); err != nil {
				return err
			}
			if sc == nil {
				continue
			}

			// Skip scenes with no markers - there is nothing to submit.
			markers, err := s.Repository.SceneMarker.FindBySceneID(ctx, sc.ID)
			if err != nil {
				return fmt.Errorf("counting markers for scene %d: %w", sc.ID, err)
			}
			if len(markers) == 0 {
				continue
			}

			// Skip scenes bearing the skip-submit tag.
			if skipTagID != 0 {
				if err := sc.LoadTagIDs(ctx, s.Repository.Scene); err != nil {
					return fmt.Errorf("loading tag ids for scene %d: %w", sc.ID, err)
				}
				if slices.Contains(sc.TagIDs.List(), skipTagID) {
					continue
				}
			}

			scenes = append(scenes, sc)
		}
		return nil
	})

	return scenes, err
}

// markerSyncSkipSubmitTagID returns the id of the skip-submit tag, or 0 when the
// tag does not exist. It must be called inside a read transaction.
func (s *Manager) markerSyncSkipSubmitTagID(ctx context.Context) (int, error) {
	const nocase = true
	tag, err := s.Repository.Tag.FindByName(ctx, markerSyncSkipSubmitTagName, nocase)
	if err != nil {
		return 0, fmt.Errorf("finding skip-submit tag: %w", err)
	}
	if tag == nil {
		return 0, nil
	}
	return tag.ID, nil
}
