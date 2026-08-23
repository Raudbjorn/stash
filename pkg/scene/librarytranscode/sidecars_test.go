package librarytranscode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

func TestPlaceSidecar(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.funscript")
	dst := filepath.Join(dir, "b.funscript")
	if err := os.WriteFile(src, []byte("script"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := PlaceSidecar(src, dst)
	if err != nil || res != PlaceCreated {
		t.Fatalf("first place res=%v err=%v", res, err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "script" {
		t.Fatalf("copied %q", got)
	}

	res, err = PlaceSidecar(src, dst)
	if err != nil || res != PlaceReused {
		t.Fatalf("identical dest should reuse, res=%v err=%v", res, err)
	}

	if err := os.WriteFile(dst, []byte("other"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = PlaceSidecar(src, dst)
	if err == nil || res != PlaceSkipped {
		t.Fatalf("differing dest should error, res=%v err=%v", res, err)
	}

	res, err = PlaceSidecar(filepath.Join(dir, "missing"), dst)
	if err != nil || res != PlaceSkipped {
		t.Fatalf("missing src should skip, res=%v err=%v", res, err)
	}
}

func TestRelocateFunscript(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srcVideo := filepath.Join(dir, "clip.mp4")
	dstVideo := filepath.Join(dir, "clip_480p.mp4")
	if err := os.WriteFile(filepath.Join(dir, "clip.funscript"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	dest, err := RelocateFunscript(srcVideo, dstVideo)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "clip_480p.funscript")
	if dest != want {
		t.Fatalf("dest = %q, want %q", dest, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatal(err)
	}

	dest, err = RelocateFunscript(srcVideo, dstVideo)
	if err != nil {
		t.Fatal(err)
	}
	if dest != "" {
		t.Fatalf("reuse must not report created path, got %q", dest)
	}
}

func TestRelocateCaption(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srcVideo := filepath.Join(dir, "clip.mkv")
	dstVideo := filepath.Join(dir, "clip_360p.mp4")
	srcCap := filepath.Join(dir, "clip.en.srt")
	if err := os.WriteFile(srcCap, []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	relocated, dest, created, err := RelocateCaption(srcVideo, dstVideo, models.VideoCaption{
		LanguageCode: "en",
		Filename:     "clip.en.srt",
		CaptionType:  "srt",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "clip_360p.en.srt")
	if dest != want || !created {
		t.Fatalf("dest = %q created=%v, want %q true", dest, created, want)
	}
	if relocated == nil || relocated.Filename != "clip_360p.en.srt" || relocated.LanguageCode != "en" {
		t.Fatalf("relocated = %+v", relocated)
	}
}

func TestRelocateCaptionExistingDestRollbackSafe(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srcVideo := filepath.Join(dir, "clip.mkv")
	dstVideo := filepath.Join(dir, "clip_360p.mp4")
	existing := []byte("preexisting caption\n")
	dstCap := filepath.Join(dir, "clip_360p.en.srt")
	if err := os.WriteFile(filepath.Join(dir, "clip.en.srt"), existing, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dstCap, existing, 0o644); err != nil {
		t.Fatal(err)
	}

	relocated, dest, created, err := RelocateCaption(srcVideo, dstVideo, models.VideoCaption{
		LanguageCode: "en",
		Filename:     "clip.en.srt",
		CaptionType:  "srt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dest != dstCap || created {
		t.Fatalf("reuse dest=%q created=%v", dest, created)
	}
	if relocated == nil || relocated.Filename != "clip_360p.en.srt" {
		t.Fatalf("relocated = %+v", relocated)
	}

	// Simulate rewrite defer: only delete paths marked created.
	var copiedSidecars []string
	if created {
		copiedSidecars = append(copiedSidecars, dest)
	}
	for _, p := range copiedSidecars {
		_ = os.Remove(p)
	}

	got, err := os.ReadFile(dstCap)
	if err != nil {
		t.Fatalf("pre-existing caption deleted on rollback: %v", err)
	}
	if string(got) != string(existing) {
		t.Fatalf("caption mutated: %q", got)
	}
}

func TestRelocateCaptionCollisionDifferentContents(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	srcVideo := filepath.Join(dir, "clip.mkv")
	dstVideo := filepath.Join(dir, "clip_360p.mp4")
	if err := os.WriteFile(filepath.Join(dir, "clip.en.srt"), []byte("source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clip_360p.en.srt"), []byte("other\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, created, err := RelocateCaption(srcVideo, dstVideo, models.VideoCaption{
		LanguageCode: "en",
		Filename:     "clip.en.srt",
		CaptionType:  "srt",
	})
	if err == nil || created {
		t.Fatalf("expected collision error, created=%v err=%v", created, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "clip_360p.en.srt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "other\n" {
		t.Fatalf("collision overwrote dest: %q", got)
	}
}
