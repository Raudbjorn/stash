package embedding

import (
	"errors"
	"os"
	"testing"
)

// candidateLibraryPaths mirrors the same list embedding_scorer_test.go (in
// the metadata package) and internal/manager/name_plausibility_scorer.go
// check, so this test runs wherever those do and skips (not fails)
// everywhere else.
var candidateLibraryPaths = []string{
	os.Getenv("ONNXRUNTIME_LIB_PATH"),
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so.1",
	"/usr/local/lib/libonnxruntime.so",
}

func loadTestModel(t *testing.T) *Model {
	t.Helper()

	for _, path := range candidateLibraryPaths {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err != nil {
			continue
		}
		model, err := Load(path)
		if err != nil {
			continue
		}
		return model
	}

	t.Skip("no onnxruntime shared library found; set ONNXRUNTIME_LIB_PATH to run this test")
	return nil
}

// TestModel_EmbedAfterClose pins down the safety mechanism
// internal/manager/name_plausibility_scorer.go's live reload depends on: a
// Model reference held by an in-flight job must not run inference against
// a destroyed onnxruntime session once a Settings-triggered reload has
// closed it out from under that job.
func TestModel_EmbedAfterClose(t *testing.T) {
	model := loadTestModel(t)

	if _, err := model.Embed("Alex Turner"); err != nil {
		t.Fatalf("Embed before Close: unexpected error: %v", err)
	}

	if err := model.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := model.Embed("Alex Turner"); !errors.Is(err, ErrModelClosed) {
		t.Fatalf("Embed after Close: err = %v, want ErrModelClosed", err)
	}
}

// TestModel_CloseTwice confirms Close is idempotent - the reload path in
// internal/manager/name_plausibility_scorer.go only ever closes a given
// Model once today, but a double-close must stay safe rather than double-
// freeing the underlying onnxruntime session/environment.
func TestModel_CloseTwice(t *testing.T) {
	model := loadTestModel(t)

	if err := model.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := model.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
