package manager

import (
	"context"
	"fmt"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/generate"
)

// GenerateClipTask generates a bounded video file for a clip's
// [seconds, end_seconds] range from its parent scene's primary video file.
type GenerateClipTask struct {
	repository          models.Repository
	Clip                *models.Clip
	Overwrite           bool
	fileNamingAlgorithm models.HashAlgorithm

	generator *generate.Generator
}

func (t *GenerateClipTask) GetDescription() string {
	return fmt.Sprintf("Generating video for clip ID %d", t.Clip.ID)
}

func (t *GenerateClipTask) Start(ctx context.Context) {
	var scene *models.Scene
	if err := t.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		scene, err = t.repository.Scene.Find(ctx, t.Clip.SceneID)
		if err != nil {
			return err
		}
		if scene == nil {
			return fmt.Errorf("scene with id %d not found", t.Clip.SceneID)
		}
		return scene.LoadPrimaryFile(ctx, t.repository.File)
	}); err != nil {
		logger.Errorf("[generator] error finding scene for clip %d: %v", t.Clip.ID, err)
		return
	}

	videoFile := scene.Files.Primary()
	if videoFile == nil {
		logger.Errorf("[generator] scene %d has no primary video file; cannot generate clip %d", scene.ID, t.Clip.ID)
		return
	}

	sceneHash := scene.GetHash(t.fileNamingAlgorithm)
	if sceneHash == "" {
		logger.Errorf("[generator] scene %d has no hash; cannot generate clip %d", scene.ID, t.Clip.ID)
		return
	}

	// ensure the destination folder exists. Use EnsureDirAll (recursive) because
	// the parent <generated>/clips directory is not created at startup like the
	// other generated subdirectories are.
	clipsFolder := instance.Paths.Clips.GetFolderPath(sceneHash)
	if err := fsutil.EnsureDirAll(clipsFolder); err != nil {
		logger.Errorf("could not create the clips folder (%v): %v", clipsFolder, err)
		return
	}

	if err := t.generator.ClipVideo(ctx, videoFile.Path, sceneHash, t.Clip.ID, t.Clip.Seconds, t.Clip.EndSeconds, instance.Config.GetPreviewAudio()); err != nil {
		logger.Errorf("[generator] failed to generate clip video for clip %d: %v", t.Clip.ID, err)
		logErrorOutput(err)
	}
}
