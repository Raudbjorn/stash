package entity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStatusCachesValidationUntilSignatureChanges(t *testing.T) {
	const key = "gliner-small-v2.1-int8"
	contents := []byte("pinned model")
	digest := sha256.Sum256(contents)
	original := Catalog[0]
	Catalog[0].Artifacts = []Artifact{{
		RemotePath: "onnx/model_int8.onnx",
		LocalPath:  "model.onnx",
		Size:       int64(len(contents)),
		SHA256:     hex.EncodeToString(digest[:]),
	}}
	t.Cleanup(func() { Catalog[0] = original })

	cachePath := t.TempDir()
	bundlePath := BundlePath(cachePath, key)
	require.NoError(t, os.MkdirAll(bundlePath, 0o755))
	modelPath := filepath.Join(bundlePath, "model.onnx")
	require.NoError(t, os.WriteFile(modelPath, contents, 0o600))

	status := Status(cachePath, key)
	require.Equal(t, ModelReady, status.State)

	// Corrupt the file's contents while restoring its original size and
	// modification time. Status() must reuse the cached validation for an
	// unchanged filesystem signature rather than re-hashing every call.
	info, err := os.Stat(modelPath)
	require.NoError(t, err)
	corrupted := append([]byte(nil), contents...)
	corrupted[0] ^= 0xFF
	require.NoError(t, os.WriteFile(modelPath, corrupted, 0o600))
	require.NoError(t, os.Chtimes(modelPath, info.ModTime(), info.ModTime()))

	status = Status(cachePath, key)
	assert.Equal(t, ModelReady, status.State,
		"an unchanged size/mtime signature should short-circuit re-hashing")

	// Advancing the modification time changes the cheap signature, which
	// forces a fresh hash and now surfaces the corruption.
	require.NoError(t, os.Chtimes(modelPath, time.Now(), time.Now()))
	status = Status(cachePath, key)
	assert.Equal(t, ModelInvalid, status.State)
}
