package paths

import (
	"path/filepath"
	"strconv"
)

type clipPaths struct {
	generatedPaths
}

func newClipPaths(p Paths) *clipPaths {
	cp := clipPaths{
		generatedPaths: *p.Generated,
	}
	return &cp
}

// GetFolderPath returns the directory for a parent scene's generated clips,
// keyed by the scene's hash.
func (cp *clipPaths) GetFolderPath(sceneChecksum string) string {
	return filepath.Join(cp.Clips, sceneChecksum)
}

// GetClipVideoPath returns the path to the generated video file for a clip.
func (cp *clipPaths) GetClipVideoPath(sceneChecksum string, clipID int) string {
	return filepath.Join(cp.GetFolderPath(sceneChecksum), strconv.Itoa(clipID)+".mp4")
}
