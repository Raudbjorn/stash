package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/transcodebenefit"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

type ScoreTranscodeBenefitInput struct {
	SceneIDs []string `json:"sceneIDs"`
}

type ScoreTranscodeBenefitJob struct {
	repository models.Repository
	input      ScoreTranscodeBenefitInput
}

func (j *ScoreTranscodeBenefitJob) Execute(ctx context.Context, progress *job.Progress) error {
	r := j.repository

	var scenes []*models.Scene
	var sizes []int64

	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		if len(j.input.SceneIDs) == 0 {
			scenes, err = r.Scene.All(ctx)
			if err != nil {
				return err
			}
		} else {
			ids, err := stringslice.StringSliceToIntSlice(j.input.SceneIDs)
			if err != nil {
				return fmt.Errorf("converting scene ids: %w", err)
			}
			scenes, err = r.Scene.FindMany(ctx, ids)
			if err != nil {
				return err
			}
		}

		for _, s := range scenes {
			if err := s.LoadPrimaryFile(ctx, r.File); err != nil {
				logger.Errorf("Error loading primary file for scene %d: %v", s.ID, err)
			}
		}

		sizes, err = r.File.PrimaryVideoFileSizes(ctx)
		return err
	}); err != nil {
		return err
	}

	p80 := transcodebenefit.SizeP80(sizes)
	hwDest := instance.FFMpeg != nil && (instance.FFMpeg.HasHWCodec(ffmpeg.VideoCodecN264) || instance.FFMpeg.HasHWCodec(ffmpeg.VideoCodecN264H))
	progress.SetTotal(len(scenes))

	const scoreChunkSize = 100
	for start := 0; start < len(scenes); start += scoreChunkSize {
		if job.IsCancelled(ctx) {
			return nil
		}
		end := start + scoreChunkSize
		if end > len(scenes) {
			end = len(scenes)
		}
		chunk := scenes[start:end]
		if err := r.WithTxn(ctx, func(ctx context.Context) error {
			now := time.Now()
			for _, s := range chunk {
				if job.IsCancelled(ctx) {
					return nil
				}
				if !s.Files.PrimaryLoaded() {
					progress.Increment()
					continue
				}
				primary := s.Files.Primary()
				if primary == nil {
					progress.Increment()
					continue
				}
				level := transcodebenefit.Level(models.GetMinResolution(primary), primary.Size, p80, hwDest)
				partial := models.ScenePartial{
					TranscodeBenefit: models.NewOptionalString(string(level)),
					UpdatedAt:        models.NewOptionalTime(now),
				}
				if _, err := r.Scene.UpdatePartial(ctx, s.ID, partial); err != nil {
					return fmt.Errorf("updating transcode_benefit for scene %d: %w", s.ID, err)
				}
				progress.Increment()
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}
