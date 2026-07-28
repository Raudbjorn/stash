package manager

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSceneCutInput(t *testing.T) {
	tempDir := t.TempDir()
	existingPath := filepath.Join(tempDir, "scene.mp4")
	if err := os.WriteFile(existingPath, []byte("video"), 0o600); err != nil {
		t.Fatalf("writing scene input: %v", err)
	}

	tests := []struct {
		name        string
		path        string
		unavailable bool
	}{
		{name: "existing file", path: existingPath},
		{name: "missing file", path: filepath.Join(tempDir, "missing.mp4"), unavailable: true},
		{name: "directory", path: tempDir, unavailable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSceneCutInput(tt.path)
			if tt.unavailable {
				if !errors.Is(err, errSceneCutInputUnavailable) {
					t.Fatalf("validateSceneCutInput() error = %v, want errSceneCutInputUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateSceneCutInput() error = %v, want nil", err)
			}
		})
	}
}
