// Package pyhost carries the Python plugin host runtime inside the Stash
// binary and unpacks it on demand.
//
// Embedding rather than shipping a directory keeps the single-binary property
// that motivated this whole port: there is nothing to install alongside Stash
// and no way for the runtime to drift out of step with the Go code that speaks
// to it over D-Bus.
package pyhost

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The all: prefix matters. Without it Go's embed skips files beginning with an
// underscore, which would silently drop every __init__.py and __main__.py and
// leave an unimportable package tree.
//
//go:embed all:runtime
var runtimeFS embed.FS

const (
	// runtimeRoot is the directory within the embedded FS.
	runtimeRoot = "runtime"
	// stampName records which build produced the extracted tree.
	stampName = ".stash-ai-runtime"
	// EntryModule is the package the host is started as.
	EntryModule = "stash_ai_host"
)

// FS exposes the embedded runtime for callers that want to read it without
// extracting - the plugin doctor lists what it would install.
func FS() fs.FS {
	sub, err := fs.Sub(runtimeFS, runtimeRoot)
	if err != nil {
		// Impossible: the path is a compile-time constant that embed verified.
		panic(fmt.Sprintf("pyhost: embedded runtime is malformed: %v", err))
	}
	return sub
}

// Version is a content hash of the embedded runtime.
//
// A hash rather than a hand-maintained number, because the failure it guards
// against is forgetting to bump one: editing a Python file and getting a stale
// extracted copy would produce confusing behaviour with no obvious cause.
func Version() (string, error) {
	sum := sha256.New()

	// Hashed in sorted order with names included, so a rename changes the hash
	// even when no file contents do.
	var paths []string
	err := fs.WalkDir(FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(paths)

	for _, path := range paths {
		data, err := fs.ReadFile(FS(), path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(sum, "%s\x00%d\x00", path, len(data))
		sum.Write(data)
	}

	return hex.EncodeToString(sum.Sum(nil))[:16], nil
}

// Extract writes the runtime to dir, replacing it if it is stale or damaged.
//
// Returns the version now on disk. Extraction is idempotent: the common path
// is a stamp comparison and no writes at all.
func Extract(dir string) (string, error) {
	version, err := Version()
	if err != nil {
		return "", err
	}

	if current, err := readStamp(dir); err == nil && current == version {
		// Trust the stamp only if the entry point actually exists; a partial
		// delete would otherwise leave an unusable tree looking current.
		if _, err := os.Stat(filepath.Join(dir, EntryModule, "__main__.py")); err == nil {
			return version, nil
		}
	}

	// Extract beside the target and swap, so an interrupted write never leaves
	// a half-updated runtime that would fail at import time.
	parent := filepath.Dir(dir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create runtime parent: %w", err)
	}

	staging, err := os.MkdirTemp(parent, ".runtime-*")
	if err != nil {
		return "", fmt.Errorf("stage runtime: %w", err)
	}
	defer os.RemoveAll(staging)

	if err := writeTree(staging); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, stampName), []byte(version), 0o644); err != nil {
		return "", fmt.Errorf("write runtime stamp: %w", err)
	}

	// Rename is not atomic over a populated directory, so the old tree goes
	// first. The window is small and recovery is simply extracting again.
	if err := os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove stale runtime: %w", err)
	}
	if err := os.Rename(staging, dir); err != nil {
		return "", fmt.Errorf("install runtime: %w", err)
	}

	return version, nil
}

func writeTree(dest string) error {
	return fs.WalkDir(FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Reject anything that would escape the destination. The embedded FS is
		// ours, so this is belt and braces rather than a live threat - but it
		// costs nothing and this code also gets read as a template.
		if path != "." && (strings.Contains(path, "..") || filepath.IsAbs(path)) {
			return fmt.Errorf("refusing suspicious runtime path %q", path)
		}

		target := filepath.Join(dest, filepath.FromSlash(path))
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}

		data, err := fs.ReadFile(FS(), path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func readStamp(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, stampName))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// EntryPoint returns the host entry script inside an extracted runtime.
func EntryPoint(dir string) string {
	return filepath.Join(dir, EntryModule, "__main__.py")
}

// ErrNotExtracted reports that the runtime is missing from a directory.
var ErrNotExtracted = errors.New("plugin host runtime is not extracted")

// Verify reports whether dir holds a usable, current runtime.
func Verify(dir string) error {
	if _, err := os.Stat(EntryPoint(dir)); err != nil {
		return ErrNotExtracted
	}

	version, err := Version()
	if err != nil {
		return err
	}
	current, err := readStamp(dir)
	if err != nil {
		return ErrNotExtracted
	}
	if current != version {
		return fmt.Errorf("runtime is version %s, this build ships %s", current, version)
	}
	return nil
}
