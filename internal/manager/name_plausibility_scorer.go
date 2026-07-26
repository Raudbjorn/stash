package manager

import (
	"os"
	"sync"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/embedding"
)

// onnxRuntimeLibraryPaths are the common install locations checked, in
// order, for a usable onnxruntime shared library, once the configured path
// (Settings > System, or STASH_ONNXRUNTIME_LIB_PATH as a headless/pre-
// config fallback) has been checked - see findOnnxRuntimeLibrary. These
// cover this fork's actual targets: Alpine's own "onnxruntime" apk package,
// and a system-wide glibc install on a native Linux dev machine.
//
// STASH_ONNXRUNTIME_LIB_PATH is a distinct, production-facing knob from the
// test-only ONNXRUNTIME_LIB_PATH read by embedding_scorer_test.go, which
// opts that integration test into running locally/in CI environments that
// happen to have the library installed somewhere nonstandard - the two are
// not meant to be the same variable.
var onnxRuntimeLibraryPaths = []string{
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so.1",
	"/usr/local/lib/libonnxruntime.so",
}

var (
	// namePlausibilityReloadMu serializes every load/reload attempt (the
	// first lazy load and any later explicit reload triggered by a Settings
	// change) so they can never race each other or double-load.
	namePlausibilityReloadMu sync.Mutex
	namePlausibilityLoaded   bool

	// namePlausibilityScorerMu guards the two vars below so concurrent
	// readers (getNamePlausibilityScorer, called once per analyzer job) never
	// block on more than the brief pointer swap a reload performs.
	namePlausibilityScorerMu sync.RWMutex
	namePlausibilityScorer   metadata.NamePlausibilityScorer
	namePlausibilityModel    *embedding.Model
)

// getNamePlausibilityScorer returns the process-wide NamePlausibilityScorer
// for the scene metadata analyzer, lazily loading the embedding model on
// first use.
//
// This is a singleton rather than per-job state because onnxruntime_go's
// environment is a genuine process-global resource - InitializeEnvironment
// errors if called a second time without an intervening DestroyEnvironment.
// See reloadNamePlausibilityScorer for how the singleton is swapped out
// live when the configured library path changes.
//
// Falls back to HeuristicNamePlausibilityScorer if the library can't be
// found or loaded, or the embedding scorer's reference exemplars fail to
// embed. This is an expected, non-error state on any machine without
// onnxruntime installed, logged at Info level, not a failure of the
// analyzer task.
func getNamePlausibilityScorer() metadata.NamePlausibilityScorer {
	namePlausibilityReloadMu.Lock()
	if !namePlausibilityLoaded {
		doReloadNamePlausibilityScorer()
		namePlausibilityLoaded = true
	}
	namePlausibilityReloadMu.Unlock()

	namePlausibilityScorerMu.RLock()
	defer namePlausibilityScorerMu.RUnlock()
	return namePlausibilityScorer
}

// reloadNamePlausibilityScorer forces a fresh reload of the name-plausibility
// scorer, re-resolving the ONNX Runtime library path from current config.
// Called by Manager.RefreshNamePlausibilityScorer when the configured path
// changes, so a Settings change takes effect on the next analyzer job run
// rather than requiring a process restart.
func reloadNamePlausibilityScorer() {
	namePlausibilityReloadMu.Lock()
	defer namePlausibilityReloadMu.Unlock()

	doReloadNamePlausibilityScorer()
	namePlausibilityLoaded = true
}

// doReloadNamePlausibilityScorer does the actual close-old/resolve/load-new
// work. Callers must hold namePlausibilityReloadMu.
//
// The old model is closed - which destroys the process-global onnxruntime
// environment - before a new one is loaded, since InitializeEnvironment
// cannot run again until the previous environment is destroyed. This means
// there's a brief window, bounded by how long the new model takes to load,
// where the scorer is the heuristic fallback; that's an acceptable
// consequence of the same "never a hard failure" philosophy that governs
// every other failure path here.
func doReloadNamePlausibilityScorer() {
	namePlausibilityScorerMu.Lock()
	oldModel := namePlausibilityModel
	namePlausibilityScorer = metadata.HeuristicNamePlausibilityScorer{}
	namePlausibilityModel = nil
	namePlausibilityScorerMu.Unlock()

	if oldModel != nil {
		if err := oldModel.Close(); err != nil {
			logger.Warnf("[scene metadata] error closing previous embedding model: %v", err)
		}
	}

	libPath, ok := findOnnxRuntimeLibrary()
	if !ok {
		logger.Infof("[scene metadata] onnxruntime shared library not found; using heuristic name-plausibility scorer")
		return
	}

	model, err := embedding.Load(libPath)
	if err != nil {
		logger.Infof("[scene metadata] failed to load embedding model from %s (%v); using heuristic name-plausibility scorer", libPath, err)
		return
	}

	scorer, err := metadata.NewEmbeddingNamePlausibilityScorer(model)
	if err != nil {
		logger.Warnf("[scene metadata] failed to initialize embedding-based name-plausibility scorer (%v); using heuristic name-plausibility scorer", err)
		model.Close()
		return
	}

	namePlausibilityScorerMu.Lock()
	namePlausibilityScorer = scorer
	namePlausibilityModel = model
	namePlausibilityScorerMu.Unlock()
	logger.Infof("[scene metadata] using embedding-based name-plausibility scorer (%s)", libPath)
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
