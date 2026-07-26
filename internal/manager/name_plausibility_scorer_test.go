package manager

import (
	"os"
	"path/filepath"
	"testing"
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
