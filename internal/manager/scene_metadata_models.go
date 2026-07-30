package manager

import (
	"context"
	"errors"
	"os"
	"sync"

	ort "github.com/yalue/onnxruntime_go"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/embedding"
	"github.com/stashapp/stash/pkg/scene/metadata/gliner"
)

// onnxRuntimeLibraryPaths are checked after the configured
// Settings > System path and the STASH_ONNXRUNTIME_LIB_PATH headless fallback.
// They preserve the native Linux and Alpine defaults used by the former
// MiniLM-only lifecycle. ONNXRUNTIME_LIB_PATH remains a separate test-only
// opt-in used by the embedding integration tests.
var onnxRuntimeLibraryPaths = []string{
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so.1",
	"/usr/local/lib/libonnxruntime.so",
}

var errSceneMetadataEntityExtractorUnavailable = errors.New("scene metadata entity extractor unavailable")

// sceneMetadataModelService owns every ONNX session used by scene metadata.
// The ONNX Runtime environment is process-global, so inference holds the read
// lock while reload holds the write lock across session teardown and rebuild.
type sceneMetadataModelService struct {
	mu sync.RWMutex

	loaded             bool
	environmentLoaded  bool
	embeddingModel     *embedding.Model
	entityExtractor    *gliner.Model
	plausibilityScorer metadata.NamePlausibilityScorer
}

var sceneMetadataModels = &sceneMetadataModelService{
	plausibilityScorer: metadata.HeuristicNamePlausibilityScorer{},
}

func (s *sceneMetadataModelService) ensureLoaded() {
	s.mu.RLock()
	loaded := s.loaded
	s.mu.RUnlock()
	if loaded {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.loaded {
		s.rebuildLocked()
	}
}

func (s *sceneMetadataModelService) Score(text string) float64 {
	s.ensureLoaded()
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.plausibilityScorer.Score(text)
}

func (s *sceneMetadataModelService) Extract(ctx context.Context, text string, labels []string, threshold float32) ([]gliner.Span, error) {
	s.ensureLoaded()
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.entityExtractor == nil {
		return nil, errSceneMetadataEntityExtractorUnavailable
	}
	return s.entityExtractor.Extract(ctx, text, labels, threshold)
}

func (s *sceneMetadataModelService) reload() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuildLocked()
}

func (s *sceneMetadataModelService) rebuildLocked() {
	if s.entityExtractor != nil {
		if err := s.entityExtractor.Close(); err != nil {
			logger.Warnf("[scene metadata] error closing GLiNER model: %v", err)
		}
	}
	if s.embeddingModel != nil {
		if err := s.embeddingModel.Close(); err != nil {
			logger.Warnf("[scene metadata] error closing embedding model: %v", err)
		}
	}
	if s.environmentLoaded {
		if err := ort.DestroyEnvironment(); err != nil {
			logger.Warnf("[scene metadata] error destroying ONNX Runtime environment: %v", err)
		}
	}

	s.loaded = true
	s.environmentLoaded = false
	s.embeddingModel = nil
	s.entityExtractor = nil
	s.plausibilityScorer = metadata.HeuristicNamePlausibilityScorer{}

	libPath, ok := findOnnxRuntimeLibrary()
	if !ok {
		logger.Infof("[scene metadata] ONNX Runtime shared library not found; using heuristic name-plausibility scorer")
		return
	}

	ort.SetSharedLibraryPath(libPath)
	if err := ort.InitializeEnvironment(); err != nil {
		logger.Warnf("[scene metadata] failed to initialize ONNX Runtime from %s: %v", libPath, err)
		return
	}
	s.environmentLoaded = true

	model, err := embedding.Load()
	if err != nil {
		logger.Warnf("[scene metadata] failed to load embedding model: %v", err)
	} else {
		scorer, scorerErr := metadata.NewEmbeddingNamePlausibilityScorer(model)
		if scorerErr != nil {
			logger.Warnf("[scene metadata] failed to initialize embedding scorer: %v", scorerErr)
			_ = model.Close()
		} else {
			s.embeddingModel = model
			s.plausibilityScorer = scorer
			logger.Infof("[scene metadata] using embedding-based name-plausibility scorer (%s)", libPath)
		}
	}

	modelPath := findSceneMetadataModelPath()
	if modelPath != "" {
		extractor, extractorErr := gliner.Load(modelPath)
		if extractorErr != nil {
			logger.Warnf("[scene metadata] failed to load GLiNER model from %s: %v", modelPath, extractorErr)
		} else {
			s.entityExtractor = extractor
			logger.Infof("[scene metadata] using GLiNER entity extractor (%s)", modelPath)
		}
	}

	if s.embeddingModel == nil && s.entityExtractor == nil {
		s.destroyEnvironmentLocked()
	}
}

func (s *sceneMetadataModelService) destroyEnvironmentLocked() {
	if s.environmentLoaded {
		if err := ort.DestroyEnvironment(); err != nil {
			logger.Warnf("[scene metadata] error destroying ONNX Runtime environment: %v", err)
		}
		s.environmentLoaded = false
	}
}

func getNamePlausibilityScorer() metadata.NamePlausibilityScorer {
	return sceneMetadataModels
}

func reloadSceneMetadataModels() {
	sceneMetadataModels.reload()
}

func findSceneMetadataModelPath() string {
	if instance != nil && instance.Config != nil {
		if configured := instance.Config.GetSceneMetadataModelPath(); configured != "" {
			return configured
		}
	}
	return os.Getenv("STASH_SCENE_METADATA_MODEL_PATH")
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
