package embedding

import (
	"errors"
	"os"
	"testing"

	ort "github.com/yalue/onnxruntime_go"
)

// candidateLibraryPaths mirrors the manager's runtime lookup, so this test
// runs wherever ONNX Runtime is available and skips everywhere else.
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
		ort.SetSharedLibraryPath(path)
		if err := ort.InitializeEnvironment(); err != nil {
			continue
		}
		model, err := Load()
		if err != nil {
			_ = ort.DestroyEnvironment()
			continue
		}
		t.Cleanup(func() {
			_ = model.Close()
			_ = ort.DestroyEnvironment()
		})
		return model
	}

	t.Skip("no onnxruntime shared library found; set ONNXRUNTIME_LIB_PATH to run this test")
	return nil
}

// TestModel_EmbedAfterClose pins down safe session teardown.
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

// TestModel_CloseTwice confirms Close is idempotent.
func TestModel_CloseTwice(t *testing.T) {
	model := loadTestModel(t)

	if err := model.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := model.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
