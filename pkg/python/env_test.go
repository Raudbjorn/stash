package python

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScraperCompatibilityPathMaterialization(t *testing.T) {
	root := t.TempDir()
	first, err := materializeScraperCompatibility(root)
	if err != nil {
		t.Fatalf("materializeScraperCompatibility() error = %v", err)
	}
	second, err := materializeScraperCompatibility(root)
	if err != nil {
		t.Fatalf("second materializeScraperCompatibility() error = %v", err)
	}
	if first != second {
		t.Fatalf("materialized paths differ: %q != %q", first, second)
	}
	sum := sha256.Sum256(scraperCompatibilityModule)
	if got, want := filepath.Base(first), hex.EncodeToString(sum[:]); got != want {
		t.Fatalf("checksum directory = %q, want %q", got, want)
	}
	directoryInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory mode = %o, want 700", got)
	}
	module := filepath.Join(first, "imghdr.py")
	moduleInfo, err := os.Stat(module)
	if err != nil {
		t.Fatal(err)
	}
	if got := moduleInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("module mode = %o, want 600", got)
	}
	content, err := os.ReadFile(module)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(scraperCompatibilityModule) {
		t.Fatal("materialized module content differs from embedded content")
	}
}

func TestScraperCompatibilityPathRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	sum := sha256.Sum256(scraperCompatibilityModule)
	directory := filepath.Join(root, "stash", "python-compat", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "elsewhere"), filepath.Join(directory, "imghdr.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeScraperCompatibility(root); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("materializeScraperCompatibility() error = %v, want non-regular target error", err)
	}
}

func TestAppendPythonPathOrdering(t *testing.T) {
	t.Setenv("PYTHONPATH", "user-path")
	cmd := exec.Command("python")
	AppendPythonPath(cmd, "compat-path", "", "scraper-path")
	var got string
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "PYTHONPATH=") {
			got = strings.TrimPrefix(entry, "PYTHONPATH=")
		}
	}
	want := strings.Join([]string{"user-path", "compat-path", "scraper-path"}, string(os.PathListSeparator))
	if got != want {
		t.Fatalf("PYTHONPATH = %q, want %q", got, want)
	}
}

func TestImghdrCompatibilityMagicBytes(t *testing.T) {
	pythonPath := os.Getenv("STASH_TEST_PYTHON")
	if pythonPath == "" {
		t.Skip("STASH_TEST_PYTHON is not set")
	}
	compatibilityPath, err := materializeScraperCompatibility(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(pythonPath, "-c", `import imghdr; assert imghdr.what(None, b"\x89PNG\r\n\x1a\n") == "png"`)
	AppendPythonPath(cmd, compatibilityPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python imghdr compatibility failed: %v\n%s", err, output)
	}
}
