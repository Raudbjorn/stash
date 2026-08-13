package manager

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
)

const legacySceneMetadataEntityModelDirectory = "gliner-small-v2.1-int8-8142fb0"

func migrateSceneMetadataEntityModelConfig(cfg *config.Config) (bool, error) {
	if cfg.GetSceneMetadataEntityModel() != "" {
		return false, nil
	}
	key := config.DefaultSceneMetadataEntityModel()
	cachePath := cfg.GetCachePath()
	destination := entity.BundlePath(cachePath, key)
	if entity.Status(cachePath, key).State == entity.ModelReady {
		cfg.SetSceneMetadataEntityModel(key)
		return true, nil
	}

	legacy := filepath.Join(entity.ModelRootPath(cachePath), legacySceneMetadataEntityModelDirectory)
	if info, err := os.Stat(legacy); err == nil && info.IsDir() {
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return false, fmt.Errorf("create scene metadata model root: %w", err)
		}
		if _, err := os.Stat(destination); os.IsNotExist(err) {
			if err := os.Rename(legacy, destination); err != nil {
				return false, fmt.Errorf("migrate scene metadata model bundle: %w", err)
			}
			if entity.Status(cachePath, key).State != entity.ModelReady {
				_ = os.Rename(destination, legacy)
				logger.Infof("[scene metadata] existing model bundle is not a pinned catalog model; leaving model selection unset")
				return false, nil
			}
			cfg.SetSceneMetadataEntityModel(key)
			return true, nil
		}
	}

	entries, err := os.ReadDir(entity.ModelRootPath(cachePath))
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("inspect scene metadata model bundles: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, known := entity.FindModel(entry.Name()); !known {
			logger.Infof("[scene metadata] existing model bundle %q is not in the pinned catalog; leaving model selection unset", entry.Name())
			break
		}
	}
	return false, nil
}
