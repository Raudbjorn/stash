package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/stashapp/stash/internal/manager/config"
	hashmd5 "github.com/stashapp/stash/pkg/hash/md5"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene"
)

// funscriptExt is the file extension (case-insensitive) of funscript files.
const funscriptExt = ".funscript"

// MarkerSyncFunscriptIndex launches a background job that indexes local
// funscripts under the configured funscript path and matches each to a scene by
// filename stem. It returns the job id. When no funscript path is configured the
// job completes immediately (nothing to do).
func (s *Manager) MarkerSyncFunscriptIndex(ctx context.Context) int {
	j := job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
		cfg := config.GetInstance().GetMarkerSyncConfig()
		if cfg.FunscriptPath == "" {
			logger.Info("Marker Sync Funscript Index: no funscript path configured; nothing to do")
			return nil
		}

		logger.Infof("Marker Sync Funscript Index: indexing funscripts under %s", cfg.FunscriptPath)

		indexed, err := s.funscriptIndexWalk(ctx, cfg.FunscriptPath, progress)
		if err != nil {
			return err
		}
		logger.Infof("Marker Sync Funscript Index: indexed %d new funscript(s)", indexed)

		matched, err := s.funscriptIndexMatch(ctx)
		if err != nil {
			return err
		}
		logger.Infof("Marker Sync Funscript Index: matched %d funscript(s) to scenes", matched)
		return nil
	})

	return s.JobManager.Add(ctx, "Marker Sync Funscript Index", j)
}

// funscriptIndexWalk recursively walks root for *.funscript files and indexes any
// not already present. File IO (walk, read, hash) happens OUTSIDE any
// transaction; each new row is inserted in its own short write transaction so a
// huge library is never read under a single held transaction. It returns the
// number of newly indexed files.
func (s *Manager) funscriptIndexWalk(ctx context.Context, root string, progress *job.Progress) (int, error) {
	var paths []string
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Log and skip unreadable entries rather than aborting the whole walk.
			logger.Warnf("Marker Sync Funscript Index: walking %q: %v", path, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), funscriptExt) {
			return nil
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		return 0, fmt.Errorf("walking funscript path %q: %w", root, err)
	}

	progress.SetTotal(len(paths))

	indexed := 0
	for _, p := range paths {
		if job.IsCancelled(ctx) {
			logger.Info("Marker Sync Funscript Index: stopping due to user request")
			return indexed, nil
		}
		if s.funscriptIndexOne(ctx, p) {
			indexed++
		}
		progress.Increment()
	}
	return indexed, nil
}

// funscriptIndexOne indexes a single funscript file. It returns true when a new
// row was created. All file IO happens outside any transaction; only the
// existence probe and the insert touch the database.
func (s *Manager) funscriptIndexOne(ctx context.Context, path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		logger.Warnf("Marker Sync Funscript Index: resolving %q: %v", path, err)
		return false
	}

	var exists bool
	if err := s.Repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		exists, err = s.Repository.FunscriptIndex.ExistsByFilename(ctx, abs)
		return err
	}); err != nil {
		logger.Warnf("Marker Sync Funscript Index: checking %q: %v", abs, err)
		return false
	}
	if exists {
		return false
	}

	// File IO OUTSIDE any transaction.
	data, err := os.ReadFile(abs)
	if err != nil {
		logger.Warnf("Marker Sync Funscript Index: reading %q: %v", abs, err)
		return false
	}

	row := &models.FunscriptIndex{
		Filename: abs,
		Metadata: extractFunscriptMetadata(data),
		MD5:      hashmd5.FromBytes(data),
	}
	if err := s.Repository.WithTxn(ctx, func(ctx context.Context) error {
		return s.Repository.FunscriptIndex.Create(ctx, row)
	}); err != nil {
		logger.Warnf("Marker Sync Funscript Index: indexing %q: %v", abs, err)
		return false
	}
	logger.Debugf("Marker Sync Funscript Index: indexed %s (md5 %s)", abs, row.MD5)
	return true
}

// extractFunscriptMetadata returns the funscript's top-level "metadata" object
// as compact JSON text, or "" when the file has none or is unparseable. It
// mirrors the community plugin (which stored json.dumps(metadata)), but stores
// an absent/unreadable metadata block as "" so the submit path can omit it.
func extractFunscriptMetadata(data []byte) string {
	var doc struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	if len(doc.Metadata) == 0 || string(doc.Metadata) == "null" {
		return ""
	}
	return string(doc.Metadata)
}

// funscriptIndexMatch matches every unmatched indexed funscript to a scene whose
// primary video file basename-stem equals the funscript filename-stem. The
// unmatched set and each scene lookup run in read transactions; each match is
// persisted in its own short write transaction. It returns the number matched.
func (s *Manager) funscriptIndexMatch(ctx context.Context) (int, error) {
	var unmatched []models.FunscriptIndex
	if err := s.Repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		unmatched, err = s.Repository.FunscriptIndex.UnmatchedRows(ctx)
		return err
	}); err != nil {
		return 0, fmt.Errorf("loading unmatched funscripts: %w", err)
	}

	matched := 0
	for _, row := range unmatched {
		if job.IsCancelled(ctx) {
			logger.Info("Marker Sync Funscript Index: stopping due to user request")
			return matched, nil
		}

		sceneID, err := s.funscriptMatchScene(ctx, row.Filename)
		if err != nil {
			logger.Warnf("Marker Sync Funscript Index: matching %q: %v", row.Filename, err)
			continue
		}
		if sceneID == 0 {
			continue
		}

		if err := s.Repository.WithTxn(ctx, func(ctx context.Context) error {
			return s.Repository.FunscriptIndex.SetSceneID(ctx, row.ID, sceneID)
		}); err != nil {
			logger.Warnf("Marker Sync Funscript Index: linking %q to scene %d: %v", row.Filename, sceneID, err)
			continue
		}
		logger.Infof("Marker Sync Funscript Index: matched %s to scene %d", row.Filename, sceneID)
		matched++
	}
	return matched, nil
}

// funscriptMatchScene finds the scene whose primary video file basename-stem
// equals the funscript filename-stem, mirroring the community plugin (query
// scenes by path substring, then compare stems exactly). It returns 0 when there
// is no match. The lookup runs in a read transaction.
func (s *Manager) funscriptMatchScene(ctx context.Context, funscriptPath string) (int, error) {
	stem := funscriptStem(funscriptPath)
	if stem == "" {
		return 0, nil
	}

	var sceneID int
	err := s.Repository.WithReadTxn(ctx, func(ctx context.Context) error {
		scenes, err := scene.Query(ctx, s.Repository.Scene, &models.SceneFilterType{
			Path: &models.StringCriterionInput{
				Value:    stem,
				Modifier: models.CriterionModifierIncludes,
			},
		}, nil)
		if err != nil {
			return fmt.Errorf("querying scenes by path: %w", err)
		}

		for _, sc := range scenes {
			if sc == nil {
				continue
			}
			if err := sc.LoadFiles(ctx, s.Repository.Scene); err != nil {
				return fmt.Errorf("loading files for scene %d: %w", sc.ID, err)
			}
			for _, f := range sc.Files.List() {
				if f == nil {
					continue
				}
				bf := f.Base()
				basename := bf.Basename
				if basename == "" {
					basename = filepath.Base(bf.Path)
				}
				if funscriptStem(basename) == stem {
					sceneID = sc.ID
					return nil
				}
			}
		}
		return nil
	})
	return sceneID, err
}

// funscriptStem returns the base filename of p without its extension.
func funscriptStem(p string) string {
	base := filepath.Base(p)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// funscriptDestPath returns the funscript destination path next to videoPath:
// the video's directory + the video's stem + ".funscript".
func funscriptDestPath(videoPath string) string {
	return filepath.Join(filepath.Dir(videoPath), funscriptStem(videoPath)+funscriptExt)
}

// copyFirstFunscript copies the first readable source funscript in sources to the
// funscript destination next to videoPath, and returns that destination. It
// NEVER overwrites an existing destination (protecting user-authored scripts):
// when a funscript already exists there it returns ("", nil). This is a pure
// filesystem operation and must be called OUTSIDE any database transaction.
func copyFirstFunscript(sources []string, videoPath string) (string, error) {
	if videoPath == "" {
		return "", nil
	}
	dest := funscriptDestPath(videoPath)

	// Guard: skip entirely if the destination already exists.
	if _, err := os.Stat(dest); err == nil {
		return "", nil
	}

	for _, src := range sources {
		if src == "" {
			continue
		}
		data, err := os.ReadFile(src)
		if err != nil {
			return "", fmt.Errorf("reading funscript %q: %w", src, err)
		}
		// O_EXCL guarantees we never clobber an existing destination even under a
		// race between the Stat above and the create here.
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			if os.IsExist(err) {
				return "", nil
			}
			return "", fmt.Errorf("creating funscript %q: %w", dest, err)
		}
		if _, err := f.Write(data); err != nil {
			_ = f.Close()
			return "", fmt.Errorf("writing funscript %q: %w", dest, err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("closing funscript %q: %w", dest, err)
		}
		return dest, nil
	}
	return "", nil
}
