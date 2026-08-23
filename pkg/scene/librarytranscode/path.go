package librarytranscode

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/stashapp/stash/pkg/fsutil"
)

// FinalPath returns the destination path for a transcode of srcPath at the
// given target short-side resolution. The collision rules are:
//   - default name: <stem>_<target>p.mp4 in the source directory
//   - if that equals srcPath, OR already exists on disk, use the hashed
//     variant: <stem>_<hash>_<target>p.mp4
//   - if the hashed variant also exists, append a numeric counter
//     (_2, _3, ...) before the target suffix until a non-existing path
//     is found
//
// FinalPath chooses a path that was not present during selection. Callers must
// still use MoveFileNoReplace because another writer can win the race before
// the encoded file is moved.
func FinalPath(srcPath string, target int) string {
	dir := filepath.Dir(srcPath)
	base := filepath.Base(srcPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	suffix := fmt.Sprintf("_%dp.mp4", target)
	defaultPath := filepath.Join(dir, stem+suffix)

	if defaultPath != srcPath && pathMissing(defaultPath) {
		return defaultPath
	}

	for counter := 0; ; counter++ {
		var candidate string
		if counter == 0 {
			candidate = hashedLibraryPath(dir, stem, "", srcPath) + suffix
		} else {
			candidate = hashedLibraryPath(dir, stem, fmt.Sprintf("_%d", counter), srcPath) + suffix
		}
		if pathMissing(candidate) {
			return candidate
		}
	}
}

func pathMissing(path string) bool {
	_, err := os.Lstat(path)
	return err != nil
}

// MoveFileNoReplace moves src to dst without replacing an existing entry.
// A hard link keeps same-filesystem moves constant-time; the exclusive-copy
// fallback handles filesystems that do not support the link.
func MoveFileNoReplace(src, dst string) error {
	linkErr := os.Link(src, dst)
	if linkErr != nil {
		if copyErr := fsutil.CopyFile(src, dst); copyErr != nil {
			return fmt.Errorf("placing file without replacement: link failed: %v; copy failed: %w", linkErr, copyErr)
		}
	}
	if err := os.Remove(src); err != nil {
		_ = os.Remove(dst)
		return fmt.Errorf("removing source after placing file: %w", err)
	}
	return nil
}

func hashedLibraryPath(dir, stem, extra, srcPath string) string {
	sum := md5.Sum([]byte(srcPath))
	hash := hex.EncodeToString(sum[:])[:8]
	return filepath.Join(dir, fmt.Sprintf("%s_%s%s", stem, hash, extra))
}
