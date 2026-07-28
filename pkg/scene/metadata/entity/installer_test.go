package entity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	status := Status(t.TempDir())
	assert.Equal(t, ModelMissing, status.State)
	assert.Equal(t, ModelID, status.ModelID)
	assert.Equal(t, ModelVersion, status.Version)
}
