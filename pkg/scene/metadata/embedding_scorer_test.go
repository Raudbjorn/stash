package metadata

import (
	"os"
	"testing"

	"github.com/stashapp/stash/pkg/scene/metadata/embedding"
)

// candidateLibraryPaths are checked, in order, for a real onnxruntime shared
// library to run this test against. The analyzer job resolves the library
// path the same way at runtime (internal/manager/task_analyze_scene_metadata.go).
var candidateLibraryPaths = []string{
	os.Getenv("ONNXRUNTIME_LIB_PATH"),
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so.1",
	"/usr/local/lib/libonnxruntime.so",
}

// loadTestModel finds and loads a real onnxruntime library for integration
// testing, skipping (not failing) if none is available - this test
// exercises real inference and must not block `go test ./...` on machines
// (including CI) without onnxruntime installed.
func loadTestModel(t *testing.T) *embedding.Model {
	t.Helper()

	for _, path := range candidateLibraryPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		model, err := embedding.Load(path)
		if err != nil {
			continue
		}
		t.Cleanup(func() { model.Close() })
		return model
	}

	t.Skip("no onnxruntime shared library found; set ONNXRUNTIME_LIB_PATH to run this test")
	return nil
}

func TestEmbeddingNamePlausibilityScorer_DiscriminatesNamesFromPhrases(t *testing.T) {
	model := loadTestModel(t)

	scorer, err := NewEmbeddingNamePlausibilityScorer(model)
	if err != nil {
		t.Fatalf("NewEmbeddingNamePlausibilityScorer: %v", err)
	}

	names := []string{"Alex Turner", "Sofia Rossi", "Tom Wilson"}
	phrases := []string{"Exclusive Update", "Amazing Night", "New Scene", "Best Video"}

	for _, name := range names {
		if score := scorer.Score(name); score < 0.5 {
			t.Errorf("Score(%q) = %.3f, want >= 0.5 (plausible name)", name, score)
		}
	}
	for _, phrase := range phrases {
		if score := scorer.Score(phrase); score > 0.5 {
			t.Errorf("Score(%q) = %.3f, want <= 0.5 (implausible phrase)", phrase, score)
		}
	}
}

func TestEmbeddingNamePlausibilityScorer_RejectsNonTwoWordCandidates(t *testing.T) {
	model := loadTestModel(t)

	scorer, err := NewEmbeddingNamePlausibilityScorer(model)
	if err != nil {
		t.Fatalf("NewEmbeddingNamePlausibilityScorer: %v", err)
	}

	for _, candidate := range []string{"Jane", "Jane Middle Doe", ""} {
		if score := scorer.Score(candidate); score != 0 {
			t.Errorf("Score(%q) = %.3f, want 0", candidate, score)
		}
	}
}
