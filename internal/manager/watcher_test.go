package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupWatcherTestManager wires the package-global manager/config singletons
// that useAsVideo/useAsImage/isZip rely on, and returns a Manager for exercising
// the watcher's path-mapping methods.
func setupWatcherTestManager(t *testing.T) *Manager {
	t.Helper()
	cfg := config.InitializeEmpty() // also sets the global config instance
	m := &Manager{Config: cfg}
	prev := instance
	instance = m
	t.Cleanup(func() { instance = prev })
	return m
}

func TestShouldScheduleScan(t *testing.T) {
	dir := t.TempDir()
	m := setupWatcherTestManager(t)

	video := filepath.Join(dir, "movie.mp4")
	require.NoError(t, os.WriteFile(video, []byte("x"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "movie.srt"), []byte("x"), 0644))

	tests := []struct {
		name string
		path string
		want string
	}{
		{"video returns itself", video, video},
		{"image returns itself", filepath.Join(dir, "pic.jpg"), filepath.Join(dir, "pic.jpg")},
		{"zip returns itself", filepath.Join(dir, "g.zip"), filepath.Join(dir, "g.zip")},
		{"unrelated file ignored", filepath.Join(dir, "notes.txt"), ""},
		{"caption maps to sibling video", filepath.Join(dir, "movie.srt"), video},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, m.shouldScheduleScan(tt.path))
		})
	}
}
