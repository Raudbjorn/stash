package librarytranscode

import (
	"os"
	"path/filepath"

	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/models"
)

// CopyFileIfExists copies src to dst when src exists and dst does not.
// Returns true when a copy was performed.
func CopyFileIfExists(src, dst string) (bool, error) {
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if _, err := os.Stat(dst); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := fsutil.CopyFile(src, dst); err != nil {
		return false, err
	}
	return true, nil
}

// RelocateFunscript copies a sibling .funscript onto the new video stem.
func RelocateFunscript(srcVideo, dstVideo string) (copied string, err error) {
	src := video.GetFunscriptPath(srcVideo)
	dst := video.GetFunscriptPath(dstVideo)
	ok, err := CopyFileIfExists(src, dst)
	if err != nil || !ok {
		return "", err
	}
	return dst, nil
}

// RelocateCaption copies one caption sidecar onto the new video stem.
func RelocateCaption(srcVideo, dstVideo string, cap models.VideoCaption) (*models.VideoCaption, string, error) {
	src := cap.Path(srcVideo)
	dst := video.GetCaptionPath(dstVideo, cap.LanguageCode, cap.CaptionType)
	ok, err := CopyFileIfExists(src, dst)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		if _, statErr := os.Stat(dst); statErr != nil {
			return nil, "", nil
		}
	}
	return &models.VideoCaption{
		LanguageCode: cap.LanguageCode,
		Filename:     filepath.Base(dst),
		CaptionType:  cap.CaptionType,
	}, dst, nil
}
