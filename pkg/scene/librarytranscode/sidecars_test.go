package librarytranscode

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

func TestCopyFileIfExists(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "a.funscript")
	dst := filepath.Join(dir, "b.funscript")
	if err := os.WriteFile(src, []byte("script"), 0o644); err != nil {
		t.Fatal(err)
	}

	ok, err := CopyFileIfExists(src, dst)
	if err != nil || !ok {
		t.Fatalf("first copy ok=%v err=%v", ok, err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "script" {
		t.Fatalf("copied %q", got)
	}

	ok, err = CopyFileIfExists(src, dst)
	if err != nil || ok {
		t.Fatalf("existing dest should skip, ok=%v err=%v", ok, err)
	}

	ok, err = CopyFileIfExists(filepath.Join(dir, "missing"), dst)
	if err != nil || ok {
		t.Fatalf("missing src should skip, ok=%v err=%v", ok, err)
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

	relocated, dest, err := RelocateCaption(srcVideo, dstVideo, models.VideoCaption{
		LanguageCode: "en",
		Filename:     "clip.en.srt",
		CaptionType:  "srt",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "clip_360p.en.srt")
	if dest != want {
		t.Fatalf("dest = %q, want %q", dest, want)
	}
	if relocated == nil || relocated.Filename != "clip_360p.en.srt" || relocated.LanguageCode != "en" {
		t.Fatalf("relocated = %+v", relocated)
	}
}
