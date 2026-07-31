package pyhost

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded tree is worth asserting on directly: `go:embed runtime` without
// the all: prefix silently drops every underscore-prefixed file, which would
// produce a runtime that extracts cleanly and then fails to import.
func TestEmbedIncludesUnderscoreFiles(t *testing.T) {
	want := []string{
		"stash_ai_host/__init__.py",
		"stash_ai_host/__main__.py",
		"stash_ai_host/interfaces.xml",
		"stash_ai/__init__.py",
		"stash_ai_server/__init__.py",
	}

	for _, path := range want {
		if _, err := fs.ReadFile(FS(), path); err != nil {
			t.Errorf("embedded runtime is missing %s: %v", path, err)
		}
	}
}

func TestVersionIsStableAndContentDerived(t *testing.T) {
	first, err := Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	second, err := Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if first != second {
		t.Errorf("Version is not stable: %s then %s", first, second)
	}
	if len(first) != 16 {
		t.Errorf("Version = %q, want 16 hex characters", first)
	}
}

func TestExtractProducesUsableTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")

	version, err := Extract(dir)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if err := Verify(dir); err != nil {
		t.Fatalf("Verify after Extract: %v", err)
	}

	if _, err := os.Stat(EntryPoint(dir)); err != nil {
		t.Fatalf("entry point missing: %v", err)
	}

	// Every embedded file must land on disk; a partial extraction is the exact
	// failure this is guarding against.
	err = fs.WalkDir(FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		if _, statErr := os.Stat(filepath.Join(dir, filepath.FromSlash(path))); statErr != nil {
			t.Errorf("extracted tree is missing %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	stamp, err := os.ReadFile(filepath.Join(dir, stampName))
	if err != nil {
		t.Fatalf("read stamp: %v", err)
	}
	if strings.TrimSpace(string(stamp)) != version {
		t.Errorf("stamp = %q, want %q", stamp, version)
	}
}

func TestExtractIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")

	if _, err := Extract(dir); err != nil {
		t.Fatalf("first Extract: %v", err)
	}

	// A file the extractor did not write proves the second call short-circuited
	// rather than rebuilding the tree.
	marker := filepath.Join(dir, "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Extract(dir); err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("Extract rewrote an already-current runtime")
	}
}

// A stale stamp must force a rebuild. This is what makes upgrading the Stash
// binary enough to upgrade the runtime.
func TestExtractReplacesStaleRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if _, err := Extract(dir); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if err := os.WriteFile(filepath.Join(dir, stampName), []byte("0000000000000000"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err == nil {
		t.Fatal("Verify accepted a stale stamp")
	}

	marker := filepath.Join(dir, "marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Extract(dir); err != nil {
		t.Fatalf("re-Extract: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("stale runtime was updated in place rather than replaced")
	}
	if err := Verify(dir); err != nil {
		t.Errorf("Verify after rebuild: %v", err)
	}
}

// A stamp is not enough on its own - if the tree was partly deleted the stamp
// still reads as current, and trusting it would leave an unusable runtime.
func TestExtractRepairsDamagedTree(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	if _, err := Extract(dir); err != nil {
		t.Fatalf("Extract: %v", err)
	}

	if err := os.Remove(EntryPoint(dir)); err != nil {
		t.Fatal(err)
	}
	if err := Verify(dir); err != ErrNotExtracted {
		t.Errorf("Verify = %v, want ErrNotExtracted", err)
	}

	if _, err := Extract(dir); err != nil {
		t.Fatalf("repair Extract: %v", err)
	}
	if _, err := os.Stat(EntryPoint(dir)); err != nil {
		t.Errorf("damaged tree was not repaired: %v", err)
	}
}

func TestVerifyReportsMissingRuntime(t *testing.T) {
	if err := Verify(t.TempDir()); err != ErrNotExtracted {
		t.Errorf("Verify = %v, want ErrNotExtracted", err)
	}
}
