package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrateSceneMetadataEntityModelConfig(t *testing.T) {
	contents := []byte("pinned model")
	digest := sha256.Sum256(contents)
	original := entity.Catalog[0]
	entity.Catalog[0].Artifacts = []entity.Artifact{{
		RemotePath: "onnx/model_int8.onnx",
		LocalPath:  "model.onnx",
		Size:       int64(len(contents)),
		SHA256:     hex.EncodeToString(digest[:]),
	}}
	t.Cleanup(func() { entity.Catalog[0] = original })

	cfg := config.InitializeEmpty()
	cachePath := t.TempDir()
	cfg.SetString(config.Cache, cachePath)
	legacy := filepath.Join(entity.ModelRootPath(cachePath), legacySceneMetadataEntityModelDirectory)
	require.NoError(t, os.MkdirAll(legacy, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacy, "model.onnx"), contents, 0o600))

	changed, err := migrateSceneMetadataEntityModelConfig(cfg)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, config.DefaultSceneMetadataEntityModel(), cfg.GetSceneMetadataEntityModel())
	assert.NoDirExists(t, legacy)
	assert.DirExists(t, entity.BundlePath(cachePath, config.DefaultSceneMetadataEntityModel()))
}

func TestMigrateSceneMetadataEntityModelConfigLeavesUnknownBundleUnset(t *testing.T) {
	cfg := config.InitializeEmpty()
	cachePath := t.TempDir()
	cfg.SetString(config.Cache, cachePath)
	require.NoError(t, os.MkdirAll(filepath.Join(entity.ModelRootPath(cachePath), "custom-model"), 0o755))

	changed, err := migrateSceneMetadataEntityModelConfig(cfg)
	require.NoError(t, err)
	assert.False(t, changed)
	assert.Empty(t, cfg.GetSceneMetadataEntityModel())
}

func TestAssignmentUnknownKeyDropped(t *testing.T) {
	cfg := config.InitializeEmpty()
	previous := cfg.GetSceneMetadataEntityModelAssignments()
	manager := &Manager{Config: cfg}
	unknown := "not-in-catalog"

	require.NoError(t, manager.SceneMetadataModelAssign(
		SceneMetadataModelRoleEntityExtraction,
		&unknown,
	))
	assert.Equal(t, previous, cfg.GetSceneMetadataEntityModelAssignments())
}
