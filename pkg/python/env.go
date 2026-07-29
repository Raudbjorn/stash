package python

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed compat/imghdr.py
var scraperCompatibilityModule []byte

var (
	scraperCompatibilityOnce sync.Once
	scraperCompatibilityPath string
	scraperCompatibilityErr  error
)

// ScraperCompatibilityPath returns a cached directory containing compatibility
// modules required by legacy scraper scripts on current Python releases.
func ScraperCompatibilityPath() (string, error) {
	scraperCompatibilityOnce.Do(func() {
		cacheRoot, err := os.UserCacheDir()
		if err != nil {
			scraperCompatibilityErr = fmt.Errorf("resolving user cache directory for Python scraper compatibility: %w", err)
			return
		}
		scraperCompatibilityPath, scraperCompatibilityErr = materializeScraperCompatibility(cacheRoot)
	})
	return scraperCompatibilityPath, scraperCompatibilityErr
}

func materializeScraperCompatibility(cacheRoot string) (string, error) {
	content := scraperCompatibilityModule
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	directory := filepath.Join(cacheRoot, "stash", "python-compat", hash)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("creating Python scraper compatibility directory %q: %w", directory, err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return "", fmt.Errorf("securing Python scraper compatibility directory %q: %w", directory, err)
	}
	target := filepath.Join(directory, "imghdr.py")
	if info, statErr := os.Lstat(target); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return "", fmt.Errorf("Python scraper compatibility target %q is not a regular file", target)
		}
		existing, readErr := os.ReadFile(target)
		if readErr != nil {
			return "", fmt.Errorf("reading Python scraper compatibility target %q: %w", target, readErr)
		}
		existingSum := sha256.Sum256(existing)
		if existingSum == sum {
			if err := os.Chmod(target, 0o600); err != nil {
				return "", fmt.Errorf("securing Python scraper compatibility target %q: %w", target, err)
			}
			return directory, nil
		}
	} else if !os.IsNotExist(statErr) {
		return "", fmt.Errorf("checking Python scraper compatibility target %q: %w", target, statErr)
	}

	temporary, err := os.CreateTemp(directory, ".imghdr-*.tmp")
	if err != nil {
		return "", fmt.Errorf("creating temporary Python scraper compatibility module: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("securing temporary Python scraper compatibility module: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("writing temporary Python scraper compatibility module: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return "", fmt.Errorf("syncing temporary Python scraper compatibility module: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return "", fmt.Errorf("closing temporary Python scraper compatibility module: %w", err)
	}
	if info, statErr := os.Lstat(target); statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", fmt.Errorf("Python scraper compatibility target %q is not a regular file", target)
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return "", fmt.Errorf("rechecking Python scraper compatibility target %q: %w", target, statErr)
	}
	if err := os.Rename(temporaryName, target); err != nil {
		return "", fmt.Errorf("installing Python scraper compatibility target %q: %w", target, err)
	}
	written, err := os.ReadFile(target)
	if err != nil {
		return "", fmt.Errorf("verifying Python scraper compatibility target %q: %w", target, err)
	}
	writtenSum := sha256.Sum256(written)
	if writtenSum != sum {
		return "", fmt.Errorf("Python scraper compatibility target %q failed checksum verification", target)
	}
	return directory, nil
}

func AppendPythonPath(cmd *exec.Cmd, paths ...string) {
	entries := make([]string, 0, len(paths)+1)
	if currentValue, set := os.LookupEnv("PYTHONPATH"); set && currentValue != "" {
		entries = append(entries, currentValue)
	}
	for _, path := range paths {
		if path = strings.TrimSpace(path); path != "" {
			entries = append(entries, path)
		}
	}
	cmd.Env = append(os.Environ(), fmt.Sprintf("PYTHONPATH=%s", strings.Join(entries, string(os.PathListSeparator))))
}
