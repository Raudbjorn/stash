package python

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	maxEngineArchiveSize = 64 << 20
	maxEngineBinarySize  = 128 << 20
)

func (m *Manager) EngineStatus(ctx context.Context) ManagerStatus {
	m.engineMu.RLock()
	defer m.engineMu.RUnlock()

	goos, goarch, musl := m.platform()
	_, mappedErr := currentReleaseAssets(goos, goarch, musl)
	path := m.uvPath
	source := EngineSourceManaged
	if mappedErr != nil {
		var err error
		path, err = m.lookPath("uv")
		if err != nil {
			message := fmt.Sprintf("uv %s is not available for this platform", ManagedUVVersion)
			return ManagerStatus{Error: &message}
		}
		source = EngineSourceSystem
	}

	version, err := m.validateEngine(ctx, path)
	if err != nil {
		message := err.Error()
		return ManagerStatus{Path: stringPtr(path), Error: &message}
	}
	return ManagerStatus{Installed: true, Version: &version, Path: &path, Source: &source}
}

func (m *Manager) InstallEngine(ctx context.Context, progress func(processed, total int64)) error {
	m.engineMu.Lock()
	defer m.engineMu.Unlock()

	goos, goarch, musl := m.platform()
	assets, err := currentReleaseAssets(goos, goarch, musl)
	if err != nil {
		return err
	}
	if _, err := m.validateEngine(ctx, m.uvPath); err == nil {
		return nil
	}
	if err := m.ensureDirectories(); err != nil {
		return err
	}

	var failures []string
	for _, asset := range assets {
		if err := m.installEngineAsset(ctx, asset, progress); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		return nil
	}
	return fmt.Errorf("install uv %s: %s", ManagedUVVersion, strings.Join(failures, "; "))
}

func (m *Manager) installEngineAsset(ctx context.Context, asset releaseAsset, progress func(int64, int64)) error {
	url := "https://github.com/astral-sh/uv/releases/download/" + ManagedUVVersion + "/" + asset.Archive
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Archive, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", asset.Archive, resp.Status)
	}
	if resp.ContentLength > maxEngineArchiveSize {
		return fmt.Errorf("download %s: archive exceeds 64 MiB", asset.Archive)
	}

	archive, err := os.CreateTemp(m.binDir, ".uv-archive-*")
	if err != nil {
		return err
	}
	archivePath := archive.Name()
	defer os.Remove(archivePath)

	hasher := sha256.New()
	limited := io.LimitReader(resp.Body, maxEngineArchiveSize+1)
	written, copyErr := io.Copy(io.MultiWriter(archive, hasher), &progressReader{reader: limited, total: resp.ContentLength, update: progress})
	closeErr := archive.Close()
	if copyErr != nil {
		return fmt.Errorf("download %s: %w", asset.Archive, copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxEngineArchiveSize {
		return fmt.Errorf("download %s: archive exceeds 64 MiB", asset.Archive)
	}
	if actual := hex.EncodeToString(hasher.Sum(nil)); actual != asset.SHA256 {
		return fmt.Errorf("download %s: SHA-256 mismatch", asset.Archive)
	}

	binary, err := os.CreateTemp(m.binDir, ".uv-binary-*")
	if err != nil {
		return err
	}
	binaryPath := binary.Name()
	defer os.Remove(binaryPath)
	if err := extractEngine(asset.Archive, archivePath, binary); err != nil {
		binary.Close()
		return err
	}
	if err := binary.Close(); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(binaryPath, 0755); err != nil {
			return err
		}
	}
	if _, err := m.validateEngine(ctx, binaryPath); err != nil {
		return fmt.Errorf("validate %s: %w", asset.Archive, err)
	}
	return replaceFile(binaryPath, m.uvPath)
}

type progressReader struct {
	reader    io.Reader
	processed int64
	total     int64
	update    func(int64, int64)
}

func (r *progressReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.processed += int64(n)
	if r.update != nil && n > 0 {
		r.update(r.processed, r.total)
	}
	return n, err
}

func extractEngine(archiveName, archivePath string, destination io.Writer) error {
	if strings.HasSuffix(archiveName, ".zip") {
		reader, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer reader.Close()
		for _, file := range reader.File {
			if file.FileInfo().IsDir() || filepath.Base(file.Name) != "uv.exe" {
				continue
			}
			source, err := file.Open()
			if err != nil {
				return err
			}
			err = copyBounded(destination, source, maxEngineBinarySize, "uv executable")
			source.Close()
			return err
		}
		return errors.New("archive does not contain uv.exe")
	}

	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer gz.Close()
	tarReader := tar.NewReader(gz)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("archive does not contain uv")
		}
		if err != nil {
			return err
		}
		if header.Typeflag != tar.TypeReg || filepath.Base(header.Name) != "uv" {
			continue
		}
		if header.Size > maxEngineBinarySize {
			return errors.New("uv executable exceeds 128 MiB")
		}
		return copyBounded(destination, tarReader, maxEngineBinarySize, "uv executable")
	}
}

func copyBounded(destination io.Writer, source io.Reader, limit int64, description string) error {
	written, err := io.Copy(destination, io.LimitReader(source, limit+1))
	if err != nil {
		return err
	}
	if written > limit {
		return fmt.Errorf("%s exceeds size limit", description)
	}
	return nil
}

func replaceFile(source, destination string) error {
	backup := destination + ".backup"
	_ = os.Remove(backup)
	hadDestination := false
	if _, err := os.Stat(destination); err == nil {
		hadDestination = true
		if err := os.Rename(destination, backup); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		if hadDestination {
			_ = os.Rename(backup, destination)
		}
		return err
	}
	if hadDestination {
		_ = os.Remove(backup)
	}
	return nil
}
