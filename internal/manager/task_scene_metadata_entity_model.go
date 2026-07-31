package manager

import (
	"context"
	"fmt"
	"sync"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
)

type SceneMetadataEntityModelStatus struct {
	ModelID          string
	Version          string
	State            string
	CachePath        string
	LastError        string
	RuntimeAvailable bool
	Active           bool
}

func (s *Manager) SceneMetadataEntityModelStatus() SceneMetadataEntityModelStatus {
	status := entity.Status(s.Config.GetCachePath())
	_, runtimeAvailable := findOnnxRuntimeLibrary()
	return SceneMetadataEntityModelStatus{
		ModelID: status.ModelID, Version: status.Version, State: string(status.State),
		CachePath: status.CachePath, LastError: status.LastError,
		RuntimeAvailable: runtimeAvailable, Active: sceneMetadataEntityExtractorActive(),
	}
}

func (s *Manager) InstallSceneMetadataEntityModel(ctx context.Context) int {
	return s.JobManager.Add(ctx, "Installing scene metadata entity model...", &installSceneMetadataEntityModelJob{
		cachePath: s.Config.GetCachePath(),
	})
}

func (s *Manager) ReloadSceneMetadataEntityModel() bool {
	s.RefreshSceneMetadataEntityExtractor()
	return sceneMetadataEntityExtractorActive()
}

var sceneMetadataEntityInstallMu sync.Mutex

type installSceneMetadataEntityModelJob struct {
	cachePath string
}

func (j *installSceneMetadataEntityModelJob) Execute(ctx context.Context, progress *job.Progress) error {
	sceneMetadataEntityInstallMu.Lock()
	defer sceneMetadataEntityInstallMu.Unlock()

	libraryPath, ok := findOnnxRuntimeLibrary()
	if !ok {
		return fmt.Errorf("ONNX Runtime is required to validate the scene metadata entity model")
	}
	installer := entity.Installer{ValidateModel: func(modelPath string) error {
		return entity.ValidateModelSignature(libraryPath, modelPath)
	}}
	if err := installer.Install(ctx, j.cachePath, func(completed, total int64) {
		if total > 0 {
			progress.SetPercent(float64(completed) / float64(total))
		}
	}); err != nil {
		return err
	}
	reloadSceneMetadataEntityExtractor()
	if !sceneMetadataEntityExtractorActive() {
		return fmt.Errorf("scene metadata entity model installed but could not be activated")
	}
	progress.SetPercent(1)
	return nil
}
