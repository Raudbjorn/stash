package entity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testArtifact(data []byte) Artifact {
	sum := sha256.Sum256(data)
	return Artifact{RemotePath: "model/file", LocalPath: "file", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func TestDownloadArtifactRejectsHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer server.Close()

	err := (Installer{ResolveURL: server.URL}).downloadArtifact(context.Background(), t.TempDir(), testArtifact([]byte("model")), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

func TestDownloadArtifactRejectsOversizeAndChecksum(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("too large"))
	}))
	defer server.Close()

	err := (Installer{ResolveURL: server.URL}).downloadArtifact(context.Background(), t.TempDir(), testArtifact([]byte("small")), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exceeds 5")
}

func TestDownloadArtifactStreamsVerifiedFile(t *testing.T) {
	data := []byte("verified bundle data")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	}))
	defer server.Close()

	directory := t.TempDir()
	var progressed int64
	err := (Installer{ResolveURL: server.URL}).downloadArtifact(context.Background(), directory, testArtifact(data), func(delta int64) { progressed += delta })
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(directory, "file"))
	require.NoError(t, err)
	assert.Equal(t, data, got)
	assert.Equal(t, int64(len(data)), progressed)
}

func TestStatusReportsMissingBundle(t *testing.T) {
	const key = "gliner-small-v2.1-int8"
	cachePath := t.TempDir()
	status := Status(cachePath, key)
	assert.Equal(t, ModelMissing, status.State)
	assert.Equal(t, key, status.Key)
	assert.Equal(t, BundlePath(cachePath, key), status.CachePath)
}

func TestInstallerPreflightRejectsInsufficientDisk(t *testing.T) {
	original := availableDiskBytes
	availableDiskBytes = func(string) (int64, error) { return 1, nil }
	t.Cleanup(func() { availableDiskBytes = original })

	err := (Installer{}).Install(context.Background(), t.TempDir(), "gliner-small-v2.1-int8", nil)
	var insufficient *ErrInsufficientDisk
	require.ErrorAs(t, err, &insufficient)
	assert.Equal(t, int64(1), insufficient.Available)
	assert.Greater(t, insufficient.Required, insufficient.Available)
}

func TestInstallerStaleKeyDoesNotCleanOther(t *testing.T) {
	original := availableDiskBytes
	availableDiskBytes = func(string) (int64, error) { return 1 << 40, nil }
	t.Cleanup(func() { availableDiskBytes = original })

	cachePath := t.TempDir()
	otherBundle := BundlePath(cachePath, "gliner-medium-v2.1-int8")
	require.NoError(t, os.MkdirAll(otherBundle, 0o755))
	marker := filepath.Join(otherBundle, "keep")
	require.NoError(t, os.WriteFile(marker, []byte("preserved"), 0o600))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forced failure", http.StatusBadGateway)
	}))
	defer server.Close()

	err := (Installer{ResolveURL: server.URL}).Install(context.Background(), cachePath, "gliner-small-v2.1-int8", nil)
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrBundleMissing))
	data, readErr := os.ReadFile(marker)
	require.NoError(t, readErr)
	assert.Equal(t, []byte("preserved"), data)
}
