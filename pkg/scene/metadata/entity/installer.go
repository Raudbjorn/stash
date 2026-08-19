package entity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type InstallProgress func(completed, total int64)

// ErrInsufficientDisk reports a model install that cannot fit in the cache
// filesystem before any download is attempted.
type ErrInsufficientDisk struct {
	Required  int64
	Available int64
	Network   bool
}

func (e *ErrInsufficientDisk) Error() string {
	location := "local filesystem"
	if e.Network {
		location = "network filesystem"
	}
	return fmt.Sprintf("insufficient disk space on %s: need %d bytes, have %d bytes", location, e.Required, e.Available)
}

// Installer downloads the immutable bundle into a temporary directory and
// publishes it atomically only after checksums and the ONNX signature pass.
type Installer struct {
	Client        *http.Client
	ResolveURL    string
	ValidateModel func(modelPath string) error
}

// ValidateModelSignature verifies the pinned bundle bytes and, when configured,
// the runtime-visible ONNX graph interface.
func (i Installer) ValidateModelSignature(cachePath, key string) error {
	spec, ok := FindModel(key)
	if !ok {
		return fmt.Errorf("unknown scene metadata model key %q", key)
	}
	if err := cachedValidateBundle(BundlePath(cachePath, key), spec); err != nil {
		return err
	}
	if i.ValidateModel == nil {
		return nil
	}
	if err := i.ValidateModel(ModelPath(cachePath, key)); err != nil {
		return fmt.Errorf("validate ONNX model: %w", err)
	}
	return nil
}

func (i Installer) Install(ctx context.Context, cachePath, key string, progress InstallProgress) (err error) {
	spec, ok := FindModel(key)
	if !ok {
		return fmt.Errorf("unknown scene metadata model key %q", key)
	}
	setLoading(key, true)
	defer setLoading(key, false)
	defer func() { setLastError(key, err) }()

	destination := BundlePath(cachePath, key)
	if validateBundle(destination, spec) == nil {
		if i.ValidateModel == nil || i.ValidateModel(filepath.Join(destination, "model.onnx")) == nil {
			return nil
		}
	}

	parent := filepath.Dir(destination)
	if err = os.MkdirAll(parent, 0o755); err != nil {
		return fmt.Errorf("create model cache: %w", err)
	}
	var total int64
	for _, artifact := range spec.Artifacts {
		total += artifact.Size
	}
	if err = preflightDiskSpace(parent, total); err != nil {
		return err
	}

	temporary, err := os.MkdirTemp(parent, "."+key+"-tmp-")
	if err != nil {
		return fmt.Errorf("create model staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(temporary)
		}
	}()

	downloader := i
	if strings.TrimSpace(downloader.ResolveURL) == "" {
		downloader.ResolveURL = "https://huggingface.co/" + spec.HuggingFaceID + "/resolve/" + spec.Revision
	}
	var completed int64
	for _, artifact := range spec.Artifacts {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = downloader.downloadArtifact(ctx, temporary, artifact, func(delta int64) {
			completed += delta
			if progress != nil {
				progress(completed, total)
			}
		}); err != nil {
			return err
		}
	}

	if err = validateBundle(temporary, spec); err != nil {
		return fmt.Errorf("validate staged model bundle: %w", err)
	}
	if i.ValidateModel == nil {
		return fmt.Errorf("model signature validator is required")
	}
	if err = i.ValidateModel(filepath.Join(temporary, "model.onnx")); err != nil {
		return fmt.Errorf("validate staged ONNX model: %w", err)
	}

	backup := destination + ".previous"
	_ = os.RemoveAll(backup)
	if _, statErr := os.Stat(destination); statErr == nil {
		if err = os.Rename(destination, backup); err != nil {
			return fmt.Errorf("preserve previous model bundle: %w", err)
		}
	} else if !os.IsNotExist(statErr) {
		return fmt.Errorf("inspect previous model bundle: %w", statErr)
	}
	if err = os.Rename(temporary, destination); err != nil {
		_ = os.Rename(backup, destination)
		return fmt.Errorf("publish model bundle: %w", err)
	}
	published = true
	_ = os.RemoveAll(backup)
	return nil
}

func (i Installer) downloadArtifact(ctx context.Context, destination string, artifact Artifact, progress func(int64)) error {
	client := i.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	baseURL := strings.TrimRight(i.ResolveURL, "/")
	if baseURL == "" {
		return fmt.Errorf("model resolve URL is required")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/"+artifact.RemotePath+"?download=true", nil)
	if err != nil {
		return fmt.Errorf("create request for %s: %w", artifact.RemotePath, err)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", artifact.RemotePath, err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("download %s: HTTP %s", artifact.RemotePath, response.Status)
	}
	if response.ContentLength > artifact.Size {
		return fmt.Errorf("download %s: response size %d exceeds %d", artifact.RemotePath, response.ContentLength, artifact.Size)
	}

	filename := filepath.Join(destination, artifact.LocalPath)
	if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
		return fmt.Errorf("create directory for %s: %w", artifact.LocalPath, err)
	}
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", artifact.LocalPath, err)
	}
	hash := sha256.New()
	writer := io.MultiWriter(file, hash, progressWriter(progress))
	count, copyErr := io.Copy(writer, io.LimitReader(response.Body, artifact.Size+1))
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("stream %s: %w", artifact.RemotePath, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close %s: %w", artifact.LocalPath, closeErr)
	}
	if count != artifact.Size {
		return fmt.Errorf("download %s: got %d bytes, expected %d", artifact.RemotePath, count, artifact.Size)
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != artifact.SHA256 {
		return fmt.Errorf("download %s: checksum %s, expected %s", artifact.RemotePath, actual, artifact.SHA256)
	}
	return nil
}

type progressWriter func(int64)

func (w progressWriter) Write(p []byte) (int, error) {
	if w != nil {
		w(int64(len(p)))
	}
	return len(p), nil
}
