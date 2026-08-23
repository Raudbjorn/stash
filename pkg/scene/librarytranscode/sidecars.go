package librarytranscode

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/stashapp/stash/pkg/file/video"
	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/models"
)

// PlaceResult is the outcome of staging a sidecar next to a rewritten video.
type PlaceResult int

const (
	// PlaceSkipped means the source sidecar does not exist.
	PlaceSkipped PlaceResult = iota
	// PlaceCreated means dest was written by this call and must be rolled back on failure.
	PlaceCreated
	// PlaceReused means dest already existed and matched src. Do not delete it.
	PlaceReused
)

// PlaceSidecar copies src to dst. Missing src is a skip. An existing dest must
// be byte-identical to src; otherwise the call fails so a later rollback cannot
// destroy a user-owned file.
func PlaceSidecar(src, dst string) (PlaceResult, error) {
	srcInfo, err := os.Stat(src)
	if err != nil {
		if os.IsNotExist(err) {
			return PlaceSkipped, nil
		}
		return PlaceSkipped, err
	}

	dstInfo, err := os.Stat(dst)
	if err == nil {
		same, err := sameFile(src, dst, srcInfo.Size(), dstInfo.Size())
		if err != nil {
			return PlaceSkipped, err
		}
		if !same {
			return PlaceSkipped, fmt.Errorf("sidecar collision %s differs from %s", dst, src)
		}
		return PlaceReused, nil
	}
	if !os.IsNotExist(err) {
		return PlaceSkipped, err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return PlaceSkipped, err
	}
	if err := fsutil.CopyFile(src, dst); err != nil {
		return PlaceSkipped, err
	}
	return PlaceCreated, nil
}

func sameFile(a, b string, aSize, bSize int64) (bool, error) {
	if aSize != bSize {
		return false, nil
	}
	ab, err := os.ReadFile(a)
	if err != nil {
		return false, err
	}
	bb, err := os.ReadFile(b)
	if err != nil {
		return false, err
	}
	return bytes.Equal(ab, bb), nil
}

// RelocateFunscript copies a sibling .funscript onto the new video stem.
// created is the dest path only when this call wrote the file.
func RelocateFunscript(srcVideo, dstVideo string) (created string, err error) {
	src := video.GetFunscriptPath(srcVideo)
	dst := video.GetFunscriptPath(dstVideo)
	res, err := PlaceSidecar(src, dst)
	if err != nil || res != PlaceCreated {
		return "", err
	}
	return dst, nil
}

// RelocateCaption copies one caption sidecar onto the new video stem.
// created is true only when dest was written by this call.
func RelocateCaption(srcVideo, dstVideo string, cap models.VideoCaption) (*models.VideoCaption, string, bool, error) {
	src := cap.Path(srcVideo)
	dst := video.GetCaptionPath(dstVideo, cap.LanguageCode, cap.CaptionType)
	res, err := PlaceSidecar(src, dst)
	if err != nil {
		return nil, "", false, err
	}
	if res == PlaceSkipped {
		return nil, "", false, nil
	}
	return &models.VideoCaption{
		LanguageCode: cap.LanguageCode,
		Filename:     filepath.Base(dst),
		CaptionType:  cap.CaptionType,
	}, dst, res == PlaceCreated, nil
}
