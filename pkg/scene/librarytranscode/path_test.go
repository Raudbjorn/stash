package librarytranscode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	src := filepath.Join(dir, "foo.mp4")
	got := FinalPath(src, 480)
	want := filepath.Join(dir, "foo_480p.mp4")
	if got != want {
		t.Fatalf("FinalPath() = %q, want %q", got, want)
	}

	// Same name as source produces a hashed sibling.
	conflicting := filepath.Join(dir, "foo.mp4")
	got = FinalPath(conflicting, 480)
	if got == conflicting {
		t.Fatalf("FinalPath() returned source path: %q", got)
	}
	if !strings.HasPrefix(filepath.Base(got), "foo_") || !strings.HasSuffix(filepath.Base(got), "_480p.mp4") {
		t.Fatalf("FinalPath() = %q, want foo_<hash>_480p.mp4", got)
	}

	// Pre-existing destination triggers the hashed sibling rule.
	dup := filepath.Join(dir, "bar.mp4")
	if err := os.WriteFile(filepath.Join(dir, "bar_480p.mp4"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got = FinalPath(dup, 480)
	if got == filepath.Join(dir, "bar_480p.mp4") {
		t.Fatalf("FinalPath() returned colliding path: %q", got)
	}
	if !strings.HasPrefix(filepath.Base(got), "bar_") || !strings.HasSuffix(filepath.Base(got), "_480p.mp4") {
		t.Fatalf("FinalPath() = %q, want bar_<hash>_480p.mp4", got)
	}
	if _, err := os.Stat(got); err == nil {
		t.Fatalf("FinalPath() returned existing path: %q", got)
	}

	// Both default and hashed variants already exist: counter advances.
	if err := os.WriteFile(filepath.Join(dir, "baz_480p.mp4"), []byte("a"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	baz := filepath.Join(dir, "baz.mp4")
	got = FinalPath(baz, 480)
	if got == filepath.Join(dir, "baz_480p.mp4") {
		t.Fatalf("FinalPath() returned existing default: %q", got)
	}
	if _, err := os.Stat(got); err == nil {
		t.Fatalf("FinalPath() returned existing hashed path: %q", got)
	}
	// Both default and a hashed variant already exist: counter must keep advancing.
	if err := os.WriteFile(filepath.Join(dir, "qux_480p.mp4"), []byte("a"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	qux := filepath.Join(dir, "qux.mp4")
	// Pre-create the first hashed candidate to force counter>2.
	quxFirst := hashedLibraryPath(dir, "qux", "", qux) + "_480p.mp4"
	if err := os.WriteFile(quxFirst, []byte("a"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got = FinalPath(qux, 480)
	if got == quxFirst {
		t.Fatalf("FinalPath() returned existing hashed+2 path: %q", got)
	}
	if _, err := os.Stat(got); err == nil {
		t.Fatalf("FinalPath() returned existing path on collision chain: %q", got)
	}
}

func TestFinalPathNeverOverwritesExisting(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "foo.mp4")
	if err := os.WriteFile(filepath.Join(dir, "foo_480p.mp4"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foo_aabbccdd_480p.mp4"), []byte("existing"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	got := FinalPath(src, 480)
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("FinalPath returned existing path %q: %v", got, err)
	}
}

func TestMoveFileNoReplacePreservesRacingDestination(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	src := filepath.Join(dir, "encoded.tmp")
	dst := filepath.Join(dir, "scene_480p.mp4")
	if err := os.WriteFile(src, []byte("encoded"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, []byte("user-owned"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MoveFileNoReplace(src, dst); err == nil {
		t.Fatal("expected destination collision to fail")
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "user-owned" {
		t.Fatalf("destination changed to %q", got)
	}
	if _, err := os.Stat(src); err != nil {
		t.Fatalf("source should remain after collision: %v", err)
	}
}
