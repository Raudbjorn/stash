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

// TestReloadNamePlausibilityScorer exercises the reload path with no
// onnxruntime library available, which is the only path that runs
// unconditionally in CI. It guards against the two failure modes a reload
// mechanism can introduce: panicking when there's no previous model to
// close, and panicking or deadlocking when reloaded repeatedly (simulating
// repeated Settings saves).
func TestReloadNamePlausibilityScorer(t *testing.T) {
	originalPaths := onnxRuntimeLibraryPaths
	onnxRuntimeLibraryPaths = []string{"/does/not/exist"}
	t.Setenv("STASH_ONNXRUNTIME_LIB_PATH", "")

	t.Cleanup(func() {
		onnxRuntimeLibraryPaths = originalPaths
		namePlausibilityReloadMu.Lock()
		namePlausibilityLoaded = false
		namePlausibilityReloadMu.Unlock()
		namePlausibilityScorerMu.Lock()
		namePlausibilityScorer = nil
		namePlausibilityModel = nil
		namePlausibilityScorerMu.Unlock()
	})

	assertHeuristic := func(t *testing.T) {
		t.Helper()
		scorer := getNamePlausibilityScorer()
		if _, ok := scorer.(metadata.HeuristicNamePlausibilityScorer); !ok {
			t.Fatalf("scorer = %T, want metadata.HeuristicNamePlausibilityScorer", scorer)
		}
	}

	// First call lazily loads with no library available - should fall back.
	assertHeuristic(t)

	// A forced reload with still nothing available - no previous embedding
	// model to close, must not panic.
	reloadNamePlausibilityScorer()
	assertHeuristic(t)

	// A second forced reload, simulating a second Settings save in a row.
	reloadNamePlausibilityScorer()
	assertHeuristic(t)
}
