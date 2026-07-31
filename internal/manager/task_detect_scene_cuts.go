package manager

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/generate"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

// defaultSceneCutMarkerTagName is the tag applied as the primary tag on
// markers created by scene-cut detection.
const defaultSceneCutMarkerTagName = "Scene Cut"

// sceneCutDedupEpsilon is the window (in seconds) within which a detected
// cut is considered to already be covered by an existing marker, to avoid
// creating duplicates on repeated runs.
const sceneCutDedupEpsilon = 1.0

const (
	defaultSceneCutThreshold      = 0.4
	defaultSceneCutDownscaleWidth = 320
)

var errSceneCutInputUnavailable = errors.New("scene-cut input file is unavailable")

type DetectSceneCutsInput struct {
	// SceneIDs to process. If empty, all scenes are processed.
	SceneIDs []string `json:"sceneIDs"`
	// Threshold is the scene-change sensitivity, 0-1. Defaults to 0.4.
	Threshold float64 `json:"threshold"`
	// DownscaleWidth to analyze at, for speed. Defaults to 320. 0 disables downscaling.
	DownscaleWidth *int `json:"downscaleWidth"`
}

func (s *Manager) DetectSceneCuts(ctx context.Context, input DetectSceneCutsInput) int {
	j := &detectSceneCutsJob{
		repository: s.Repository,
		input:      input,
	}

	return s.JobManager.Add(ctx, "Detecting scene cuts...", j)
}

type detectSceneCutsJob struct {
	repository models.Repository
	input      DetectSceneCutsInput
}

func (j *detectSceneCutsJob) Execute(ctx context.Context, progress *job.Progress) error {
	r := j.repository

	threshold := j.input.Threshold
	if threshold <= 0 {
		threshold = defaultSceneCutThreshold
	}

	downscaleWidth := defaultSceneCutDownscaleWidth
	if j.input.DownscaleWidth != nil {
		downscaleWidth = *j.input.DownscaleWidth
	}

	tagID, err := j.findOrCreateMarkerTag(ctx)
	if err != nil {
		return fmt.Errorf("finding/creating scene cut marker tag: %w", err)
	}

	sceneIDs, err := stringslice.StringSliceToIntSlice(j.input.SceneIDs)
	if err != nil {
		return fmt.Errorf("parsing scene ids: %w", err)
	}

	var scenes []*models.Scene
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		if len(sceneIDs) > 0 {
			scenes, err = r.Scene.FindMany(ctx, sceneIDs)
			return err
		}

		scenes, err = r.Scene.All(ctx)
		return err
	}); err != nil {
		return fmt.Errorf("finding scenes: %w", err)
	}

	progress.SetTotal(len(scenes))

	g := &generate.Generator{
		Encoder:      instance.FFMpeg,
		FFMpegConfig: instance.Config,
		LockManager:  instance.ReadLockManager,
	}

	skippedUnavailable := 0

	for _, sc := range scenes {
		if job.IsCancelled(ctx) {
			return nil
		}

		var processErr error
		progress.ExecuteTask(fmt.Sprintf("Detecting scene cuts for %s", sc.GetTitle()), func() {
			processErr = j.processScene(ctx, g, sc, tagID, threshold, downscaleWidth)
		})

		if processErr != nil {
			switch {
			case errors.Is(processErr, errSceneCutInputUnavailable):
				skippedUnavailable++
				logger.Debugf("[scene cuts] skipping scene %d because its primary file is unavailable", sc.ID)
			case errors.Is(processErr, context.Canceled), errors.Is(processErr, context.DeadlineExceeded):
				if job.IsCancelled(ctx) {
					return nil
				}
				logger.Debugf("[scene cuts] processing scene %d was canceled: %v", sc.ID, processErr)
			default:
				logger.Errorf("[scene cuts] error processing scene %d: %v", sc.ID, processErr)
			}
		}

		progress.Increment()
	}

	if skippedUnavailable > 0 {
		logger.Warnf("[scene cuts] skipped %d scenes because their primary files are unavailable", skippedUnavailable)
	}

	return nil
}

func (j *detectSceneCutsJob) findOrCreateMarkerTag(ctx context.Context) (int, error) {
	r := j.repository

	var tagID int
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		tags, err := r.Tag.FindByNames(ctx, []string{defaultSceneCutMarkerTagName}, true)
		if err != nil {
			return err
		}
		if len(tags) > 0 {
			tagID = tags[0].ID
			return nil
		}

		newTag := models.NewTag()
		newTag.Name = defaultSceneCutMarkerTagName
		if err := r.Tag.Create(ctx, &models.CreateTagInput{Tag: &newTag}); err != nil {
			return err
		}
		tagID = newTag.ID

		return nil
	}); err != nil {
		return 0, err
	}

	return tagID, nil
}

func validateSceneCutInput(path string) error {
	exists, err := fsutil.FileExists(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return errSceneCutInputUnavailable
		}
		return fmt.Errorf("checking scene-cut input: %w", err)
	}
	if !exists {
		return errSceneCutInputUnavailable
	}
	return nil
}

func (j *detectSceneCutsJob) processScene(ctx context.Context, g *generate.Generator, sc *models.Scene, tagID int, threshold float64, downscaleWidth int) error {
	r := j.repository

	var existingMarkers []*models.SceneMarker
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		if err := sc.LoadPrimaryFile(ctx, r.File); err != nil {
			return err
		}

		markers, err := r.SceneMarker.FindBySceneID(ctx, sc.ID)
		if err != nil {
			return err
		}

		// Only dedup against this task's own prior output, not the scene's
		// manually-placed markers - otherwise a real detected cut that
		// happens to land near an unrelated user marker would be silently
		// dropped.
		for _, m := range markers {
			if m.AutoGenerated {
				existingMarkers = append(existingMarkers, m)
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("loading scene: %w", err)
	}

	videoFile := sc.Files.Primary()
	if videoFile == nil {
		return nil
	}

	if err := validateSceneCutInput(videoFile.Path); err != nil {
		return err
	}

	timestamps, err := g.DetectSceneCuts(ctx, videoFile.Path, transcoder.SceneDetectOptions{
		Threshold:      threshold,
		DownscaleWidth: downscaleWidth,
	})
	if err != nil {
		return fmt.Errorf("detecting scene cuts: %w", err)
	}

	for _, t := range timestamps {
		if isMarkerNear(existingMarkers, t) {
			continue
		}

		newMarker := models.NewSceneMarker()
		newMarker.Title = defaultSceneCutMarkerTagName
		newMarker.Seconds = t
		newMarker.SceneID = sc.ID
		newMarker.PrimaryTagID = tagID
		newMarker.AutoGenerated = true

		if err := r.WithTxn(ctx, func(ctx context.Context) error {
			return r.SceneMarker.Create(ctx, &newMarker)
		}); err != nil {
			return fmt.Errorf("creating marker at %.3fs: %w", t, err)
		}
	}

	return nil
}

func isMarkerNear(markers []*models.SceneMarker, seconds float64) bool {
	for _, m := range markers {
		if math.Abs(m.Seconds-seconds) <= sceneCutDedupEpsilon {
			return true
		}
	}
	return false
}
