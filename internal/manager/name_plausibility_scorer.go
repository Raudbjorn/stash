package manager

import (
	"context"
	"os"
	"sync"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
)

var onnxRuntimeLibraryPaths = []string{
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so.1",
	"/usr/local/lib/libonnxruntime.so",
}

var (
	sceneMetadataEntityReloadMu sync.Mutex
	sceneMetadataEntityLoaded   bool

	sceneMetadataEntityMu        sync.RWMutex
	sceneMetadataEntityExtractor *entity.Extractor
)

// The deterministic scorer remains the cheap structural guard for heuristic
// two-word candidates. Learned extraction is a separate typed evidence path.
func getNamePlausibilityScorer() metadata.NamePlausibilityScorer {
	return metadata.HeuristicNamePlausibilityScorer{}
}

type managedSceneMetadataExtractor struct{}

func (managedSceneMetadataExtractor) Extract(ctx context.Context, text string, labels []string) ([]metadata.EntitySpan, error) {
	sceneMetadataEntityMu.RLock()
	defer sceneMetadataEntityMu.RUnlock()
	if sceneMetadataEntityExtractor == nil {
		return nil, entity.ErrBundleMissing
	}
	return sceneMetadataEntityExtractor.Extract(ctx, text, labels)
}

func getSceneMetadataEntityExtractor() metadata.EntityExtractor {
	sceneMetadataEntityReloadMu.Lock()
	if !sceneMetadataEntityLoaded {
		doReloadSceneMetadataEntityExtractor()
		sceneMetadataEntityLoaded = true
	}
	sceneMetadataEntityReloadMu.Unlock()

	sceneMetadataEntityMu.RLock()
	available := sceneMetadataEntityExtractor != nil
	sceneMetadataEntityMu.RUnlock()
	if !available {
		return nil
	}
	return managedSceneMetadataExtractor{}
}
func sceneMetadataEntityExtractorActive() bool {
	sceneMetadataEntityMu.RLock()
	defer sceneMetadataEntityMu.RUnlock()
	return sceneMetadataEntityExtractor != nil
}

func reloadSceneMetadataEntityExtractor() {
	sceneMetadataEntityReloadMu.Lock()
	defer sceneMetadataEntityReloadMu.Unlock()
	doReloadSceneMetadataEntityExtractor()
	sceneMetadataEntityLoaded = true
}

// doReloadSceneMetadataEntityExtractor validates a replacement before the
// pointer swap. The shared entity runtime keeps one ref-counted ONNX
// environment, so the old session remains usable until all in-flight calls
// finish and Close runs after the swap.
func doReloadSceneMetadataEntityExtractor() {
	libPath, ok := findOnnxRuntimeLibrary()
	if !ok {
		logger.Infof("[scene metadata] onnxruntime shared library not found; deterministic analysis remains available")
		return
	}
	if instance == nil || instance.Config == nil {
		logger.Infof("[scene metadata] configuration unavailable; deterministic analysis remains available")
		return
	}
	bundlePath := entity.BundlePath(instance.Config.GetCachePath())
	replacement, err := entity.Load(libPath, bundlePath, entity.DefaultThreshold)
	if err != nil {
		logger.Infof("[scene metadata] GLiNER unavailable; deterministic analysis remains available")
		return
	}

	sceneMetadataEntityMu.Lock()
	old := sceneMetadataEntityExtractor
	sceneMetadataEntityExtractor = replacement
	sceneMetadataEntityMu.Unlock()
	if old != nil {
		if err := old.Close(); err != nil {
			logger.Warnf("[scene metadata] closing previous GLiNER session: %v", err)
		}
	}
	logger.Infof("[scene metadata] using local %s entity model", entity.ModelVersion)
}

func closeSceneMetadataEntityExtractor() {
	sceneMetadataEntityReloadMu.Lock()
	defer sceneMetadataEntityReloadMu.Unlock()
	sceneMetadataEntityMu.Lock()
	old := sceneMetadataEntityExtractor
	sceneMetadataEntityExtractor = nil
	sceneMetadataEntityMu.Unlock()
	if old != nil {
		if err := old.Close(); err != nil {
			logger.Warnf("[scene metadata] closing GLiNER session: %v", err)
		}
	}
	sceneMetadataEntityLoaded = false
}

func findOnnxRuntimeLibrary() (string, bool) {
	var configuredPath string
	if instance != nil && instance.Config != nil {
		configuredPath = instance.Config.GetOnnxRuntimeLibPath()
	}

	candidates := make([]string, 0, len(onnxRuntimeLibraryPaths)+2)
	candidates = append(candidates, configuredPath, os.Getenv("STASH_ONNXRUNTIME_LIB_PATH"))
	candidates = append(candidates, onnxRuntimeLibraryPaths...)
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}
