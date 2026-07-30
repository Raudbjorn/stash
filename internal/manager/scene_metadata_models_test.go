package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/scene/metadata"
)

func TestFindOnnxRuntimeLibrary(t *testing.T) {
	dir := t.TempDir()
	fakeLib := filepath.Join(dir, "libonnxruntime.so")
	if err := os.WriteFile(fakeLib, []byte{}, 0o644); err != nil {
		t.Fatalf("writing fake library: %v", err)
	}

	original := onnxRuntimeLibraryPaths
	t.Cleanup(func() { onnxRuntimeLibraryPaths = original })

	t.Run("finds an existing path", func(t *testing.T) {
		onnxRuntimeLibraryPaths = []string{"", "/does/not/exist", fakeLib}
		path, ok := findOnnxRuntimeLibrary()
		if !ok {
			t.Fatal("expected to find the fake library, got ok=false")
		}
		if path != fakeLib {
			t.Errorf("path = %q, want %q", path, fakeLib)
		}
	})

	t.Run("returns false when nothing exists", func(t *testing.T) {
		onnxRuntimeLibraryPaths = []string{"", "/does/not/exist", "/also/not/here"}
		_, ok := findOnnxRuntimeLibrary()
		if ok {
			t.Error("expected ok=false when no candidate path exists")
		}
	})
}

func resetSceneMetadataModelState(t *testing.T) {
	t.Helper()
	sceneMetadataModels.mu.Lock()
	defer sceneMetadataModels.mu.Unlock()
	if sceneMetadataModels.embeddingModel != nil {
		_ = sceneMetadataModels.embeddingModel.Close()
	}
	sceneMetadataModels.destroyEnvironmentLocked()
	sceneMetadataModels.loaded = false
	sceneMetadataModels.embeddingModel = nil
	sceneMetadataModels.plausibilityScorer = metadata.HeuristicNamePlausibilityScorer{}
}

// TestReloadSceneMetadataModels exercises repeated fallback reloads.
func TestReloadSceneMetadataModels(t *testing.T) {
	originalPaths := onnxRuntimeLibraryPaths
	onnxRuntimeLibraryPaths = []string{"/does/not/exist"}
	t.Setenv("STASH_ONNXRUNTIME_LIB_PATH", "")

	resetSceneMetadataModelState(t)
	t.Cleanup(func() {
		onnxRuntimeLibraryPaths = originalPaths
		resetSceneMetadataModelState(t)
	})

	assertHeuristic := func(t *testing.T) {
		t.Helper()
		got := getNamePlausibilityScorer().Score("Jane Doe")
		want := (metadata.HeuristicNamePlausibilityScorer{}).Score("Jane Doe")
		if got != want {
			t.Fatalf("score = %v, want heuristic score %v", got, want)
		}
	}

	// First call lazily loads with no library available - should fall back.
	assertHeuristic(t)

	// A forced reload with still nothing available - no previous embedding
	// model to close, must not panic.
	reloadSceneMetadataModels()
	assertHeuristic(t)

	// A second forced reload, simulating a second Settings save in a row.
	reloadSceneMetadataModels()
	assertHeuristic(t)
}
