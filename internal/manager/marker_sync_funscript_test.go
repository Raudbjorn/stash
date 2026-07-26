package manager

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// writeFile writes content to a new file under dir and returns its path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", p, err)
	}
	return p
}

// TestFunscriptMatchCopy exercises the md5-lookup + copy path used by the fetch
// task: an index row with a matching md5 points at a temp source funscript; the
// copy helper writes the funscript next to the (temp) video path and refuses to
// overwrite an existing destination.
func TestFunscriptMatchCopy(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	srcDir := t.TempDir()
	videoDir := t.TempDir()

	const md5 = "match-me-md5"
	const scriptBody = `{"actions":[{"at":0,"pos":0}],"metadata":{"creator":"x"}}`

	srcPath := writeFile(t, srcDir, "source.funscript", scriptBody)
	videoPath := filepath.Join(videoDir, "My Video.mp4") // need not exist on disk

	// Index the source funscript with the matching md5.
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		return r.FunscriptIndex.Create(ctx, &models.FunscriptIndex{
			Filename: srcPath,
			Metadata: `{"creator":"x"}`,
			MD5:      md5,
		})
	}); err != nil {
		t.Fatalf("indexing funscript: %v", err)
	}

	// Resolve source files by md5 (as applyFunscripts does), then copy.
	var sources []string
	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		rows, err := r.FunscriptIndex.FindByMD5(ctx, md5)
		if err != nil {
			return err
		}
		for _, row := range rows {
			sources = append(sources, row.Filename)
		}
		return nil
	}); err != nil {
		t.Fatalf("FindByMD5: %v", err)
	}
	if len(sources) != 1 || sources[0] != srcPath {
		t.Fatalf("sources = %v, want [%s]", sources, srcPath)
	}

	// First copy creates the destination next to the video.
	wantDest := filepath.Join(videoDir, "My Video.funscript")
	dest, err := copyFirstFunscript(sources, videoPath)
	if err != nil {
		t.Fatalf("copyFirstFunscript: %v", err)
	}
	if dest != wantDest {
		t.Fatalf("dest = %q, want %q", dest, wantDest)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if string(got) != scriptBody {
		t.Errorf("dest contents = %q, want copied source contents", string(got))
	}

	// Second copy must NOT overwrite the existing destination (dest-exists guard).
	const sentinel = "USER AUTHORED - DO NOT CLOBBER"
	if err := os.WriteFile(wantDest, []byte(sentinel), 0o644); err != nil {
		t.Fatalf("overwriting dest with sentinel: %v", err)
	}
	dest2, err := copyFirstFunscript(sources, videoPath)
	if err != nil {
		t.Fatalf("copyFirstFunscript (existing dest): %v", err)
	}
	if dest2 != "" {
		t.Errorf("second copy returned dest %q, want \"\" (skipped, existing dest)", dest2)
	}
	after, err := os.ReadFile(wantDest)
	if err != nil {
		t.Fatalf("reading dest after: %v", err)
	}
	if string(after) != sentinel {
		t.Errorf("existing destination was overwritten: %q, want sentinel preserved", string(after))
	}
}

// TestFunscriptMatchScene proves the stem match: a unique stem links to its one
// scene, while a stem shared by two scenes is ambiguous and links to NEITHER
// (returns 0) so a funscript is never linked to the wrong scene. It also proves
// the query returns all results (no 25-row cap) by seeding 30 decoy scenes.
func TestFunscriptMatchScene(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()
	s := &Manager{Repository: r}

	// makeScene creates a scene with a single video file at videoPath.
	makeScene := func(folderPath, videoPath string) int {
		var sceneID int
		if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
			folder := models.Folder{
				Path:     folderPath,
				DirEntry: models.DirEntry{ModTime: time.Now()},
			}
			if err := r.Folder.Create(ctx, &folder); err != nil {
				return err
			}
			f := &models.VideoFile{
				BaseFile: &models.BaseFile{
					Path:           videoPath,
					Basename:       filepath.Base(videoPath),
					ParentFolderID: folder.ID,
				},
			}
			if err := r.File.Create(ctx, f); err != nil {
				return err
			}
			sc := models.NewScene()
			sc.Title = videoPath
			if err := r.Scene.Create(ctx, &sc, []models.FileID{f.ID}); err != nil {
				return err
			}
			sceneID = sc.ID
			return nil
		}); err != nil {
			t.Fatalf("creating scene %q: %v", videoPath, err)
		}
		return sceneID
	}

	// One scene whose stem exactly equals uniqueStem, plus 30 decoy scenes whose
	// paths CONTAIN uniqueStem as a substring (so they all match the path query)
	// but whose stems differ (so only the first exact-matches). This exercises
	// the >25-row path: with the default 25-row page the exact match could be
	// missed; with PerPage=-1 all rows are returned and the match is found.
	const uniqueStem = "Unique Clip 0001"
	uniqueID := makeScene("/lib/unique", "/lib/unique/"+uniqueStem+".mp4")
	for i := 0; i < 30; i++ {
		name := fmt.Sprintf("%s variant %02d", uniqueStem, i)
		makeScene("/lib/decoy", "/lib/decoy/"+name+".mp4")
	}

	got, err := s.funscriptMatchScene(ctx, "/scripts/"+uniqueStem+".funscript")
	if err != nil {
		t.Fatalf("funscriptMatchScene (unique): %v", err)
	}
	if got != uniqueID {
		t.Errorf("funscriptMatchScene(unique) = %d, want %d", got, uniqueID)
	}

	// Two scenes sharing the same stem -> ambiguous -> no link.
	const sharedStem = "Shared Clip"
	makeScene("/lib/a", "/lib/a/"+sharedStem+".mp4")
	makeScene("/lib/b", "/lib/b/"+sharedStem+".mkv")

	got, err = s.funscriptMatchScene(ctx, "/scripts/"+sharedStem+".funscript")
	if err != nil {
		t.Fatalf("funscriptMatchScene (ambiguous): %v", err)
	}
	if got != 0 {
		t.Errorf("funscriptMatchScene(ambiguous) = %d, want 0 (ambiguous stem, not linked)", got)
	}
}

// TestFunscriptIndexStoreSurface exercises the FunscriptIndex store methods not
// covered by the copy/submit tests (ExistsByFilename, UnmatchedRows) against a
// live database (newTestRepository applies migration 95). This gives executed
// coverage while the tagged pkg/sqlite suite is blocked by an unrelated
// pre-existing compile error in group_test.go.
func TestFunscriptIndexStoreSurface(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	const filename = "/lib/scripts/example.funscript"
	const md5 = "surface-md5"

	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		return r.FunscriptIndex.Create(ctx, &models.FunscriptIndex{Filename: filename, MD5: md5})
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		exists, err := r.FunscriptIndex.ExistsByFilename(ctx, filename)
		if err != nil {
			return err
		}
		if !exists {
			t.Errorf("ExistsByFilename(%q) = false, want true", filename)
		}

		missing, err := r.FunscriptIndex.ExistsByFilename(ctx, "/lib/scripts/missing.funscript")
		if err != nil {
			return err
		}
		if missing {
			t.Errorf("ExistsByFilename(missing) = true, want false")
		}

		unmatched, err := r.FunscriptIndex.UnmatchedRows(ctx)
		if err != nil {
			return err
		}
		found := false
		for _, row := range unmatched {
			if row.Filename == filename {
				found = true
				if row.SceneID != nil {
					t.Errorf("newly created row has SceneID %v, want nil", row.SceneID)
				}
			}
		}
		if !found {
			t.Errorf("UnmatchedRows did not include the newly created row")
		}
		return nil
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
}

// TestBuildSceneSubmissionFunscriptHashes proves buildSceneSubmission populates
// FunscriptHashes from the index for the scene (basename, raw metadata, md5),
// and that gating it off yields none.
func TestBuildSceneSubmissionFunscriptHashes(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	// A scene to attach the funscript to.
	var sceneID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc := models.NewScene()
		sc.Title = "Funscript Scene"
		if err := r.Scene.Create(ctx, &sc, nil); err != nil {
			return err
		}
		sceneID = sc.ID
		return nil
	}); err != nil {
		t.Fatalf("creating scene: %v", err)
	}

	// An indexed funscript matched to the scene.
	const meta = `{"creator":"tester","chapters":[]}`
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		row := &models.FunscriptIndex{
			Filename: "/some/dir/My Clip.funscript",
			Metadata: meta,
			MD5:      "scene-md5",
		}
		if err := r.FunscriptIndex.Create(ctx, row); err != nil {
			return err
		}
		return r.FunscriptIndex.SetSceneID(ctx, row.ID, sceneID)
	}); err != nil {
		t.Fatalf("indexing/matching funscript: %v", err)
	}

	// With the flag on, FunscriptHashes is populated from the index.
	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc, err := r.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		s, err := buildSceneSubmission(ctx, r, sc, true)
		if err != nil {
			return err
		}
		if len(s.FunscriptHashes) != 1 {
			t.Fatalf("got %d funscript hashes, want 1", len(s.FunscriptHashes))
		}
		fh := s.FunscriptHashes[0]
		if fh.Filename != "My Clip.funscript" {
			t.Errorf("filename = %q, want basename My Clip.funscript", fh.Filename)
		}
		if fh.MD5 != "scene-md5" {
			t.Errorf("md5 = %q, want scene-md5", fh.MD5)
		}
		if fh.Metadata != meta {
			t.Errorf("metadata = %q, want raw JSON %q", fh.Metadata, meta)
		}
		return nil
	}); err != nil {
		t.Fatalf("buildSceneSubmission (enabled): %v", err)
	}

	// With the flag off, no funscript hashes are attached.
	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc, err := r.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		s, err := buildSceneSubmission(ctx, r, sc, false)
		if err != nil {
			return err
		}
		if len(s.FunscriptHashes) != 0 {
			t.Errorf("got %d funscript hashes with flag off, want 0", len(s.FunscriptHashes))
		}
		return nil
	}); err != nil {
		t.Fatalf("buildSceneSubmission (disabled): %v", err)
	}
}
