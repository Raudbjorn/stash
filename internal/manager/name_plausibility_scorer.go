package manager

import (
	"os"
	"sync"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/embedding"
)

// onnxRuntimeLibraryPaths are checked, in order, for a usable onnxruntime
// shared library. STASH_ONNXRUNTIME_LIB_PATH lets a deployment point at a
// nonstandard install; the rest are the common install locations across
// this fork's actual targets (Alpine's own "onnxruntime" apk package, and a
// system-wide glibc install on a native Linux dev machine).
//
// This is a distinct, production-facing knob from the test-only
// ONNXRUNTIME_LIB_PATH read by embedding_scorer_test.go, which opts that
// integration test into running locally/in CI environments that happen to
// have the library installed somewhere nonstandard - the two are not meant
// to be the same variable.
var onnxRuntimeLibraryPaths = []string{
	os.Getenv("STASH_ONNXRUNTIME_LIB_PATH"),
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so.1",
	"/usr/local/lib/libonnxruntime.so",
}

var (
	namePlausibilityScorerOnce sync.Once
	namePlausibilityScorer     metadata.NamePlausibilityScorer
)

// getNamePlausibilityScorer returns the process-wide NamePlausibilityScorer
// for the scene metadata analyzer, loading the embedding model on first use.
//
// This is a singleton rather than per-job state because onnxruntime_go's
// environment is a genuine process-global resource - InitializeEnvironment
// errors if called a second time without an intervening DestroyEnvironment.
// Loading once and keeping it for the process lifetime (never closed) is
// the correct shape for a model that's used repeatedly across job runs.
//
// Falls back to HeuristicNamePlausibilityScorer - and stays on it for the
// rest of the process's life - if the library can't be found or loaded, or
// the embedding scorer's reference exemplars fail to embed. This is an
// expected, non-error state on any machine without onnxruntime installed,
// logged once at Info level, not a failure of the analyzer task.
func getNamePlausibilityScorer() metadata.NamePlausibilityScorer {
	namePlausibilityScorerOnce.Do(func() {
		namePlausibilityScorer = metadata.HeuristicNamePlausibilityScorer{}

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

		logger.Infof("[scene metadata] using embedding-based name-plausibility scorer (%s)", libPath)
		namePlausibilityScorer = scorer
	})

	return namePlausibilityScorer
}

func findOnnxRuntimeLibrary() (string, bool) {
	for _, path := range onnxRuntimeLibraryPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}
