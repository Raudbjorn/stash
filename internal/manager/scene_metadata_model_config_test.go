package manager

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestAssignmentUnknownKeyRejected(t *testing.T) {
	cfg := config.InitializeEmpty()
	previous := cfg.GetSceneMetadataEntityModelAssignments()
	manager := &Manager{Config: cfg}
	unknown := "not-in-catalog"

	err := manager.SceneMetadataModelAssign(
		SceneMetadataModelRoleEntityExtraction,
		&unknown,
	)
	require.Error(t, err)
	assert.Equal(t, previous, cfg.GetSceneMetadataEntityModelAssignments())
}

func TestAssignmentUninstalledModelRejected(t *testing.T) {
	cfg := config.InitializeEmpty()
	cfg.SetString(config.Cache, t.TempDir())
	previous := cfg.GetSceneMetadataEntityModelAssignments()
	manager := &Manager{Config: cfg}
	uninstalled := "gliner-small-v2.1-int8"

	err := manager.SceneMetadataModelAssign(
		SceneMetadataModelRoleEntityExtraction,
		&uninstalled,
	)
	require.Error(t, err)
	assert.Equal(t, previous, cfg.GetSceneMetadataEntityModelAssignments())
}

// installFakeSceneMetadataBundle repins a catalog entry to a single tiny
// artifact and writes it, so entity.Status reports the model ready without
// downloading hundreds of megabytes.
func installFakeSceneMetadataBundle(t *testing.T, cachePath string, catalogIndex int) string {
	t.Helper()
	key := entity.Catalog[catalogIndex].Key
	contents := []byte("pinned model " + key)
	digest := sha256.Sum256(contents)
	original := entity.Catalog[catalogIndex]
	entity.Catalog[catalogIndex].Artifacts = []entity.Artifact{{
		RemotePath: "onnx/model_int8.onnx",
		LocalPath:  "model.onnx",
		Size:       int64(len(contents)),
		SHA256:     hex.EncodeToString(digest[:]),
	}}
	t.Cleanup(func() { entity.Catalog[catalogIndex] = original })

	bundlePath := entity.BundlePath(cachePath, key)
	require.NoError(t, os.MkdirAll(bundlePath, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlePath, "model.onnx"), contents, 0o600))
	require.Equal(t, entity.ModelReady, entity.Status(cachePath, key).State)
	return key
}

func TestAssignmentFailedActivationPreservesWorkingModel(t *testing.T) {
	cfg, sessions := setupFakeSceneMetadataSessions(t)
	cfg.SetConfigFile(filepath.Join(t.TempDir(), "config.yml"))
	cachePath := cfg.GetCachePath()
	working := installFakeSceneMetadataBundle(t, cachePath, 0)
	replacement := installFakeSceneMetadataBundle(t, cachePath, 1)

	manager := &Manager{Config: cfg}
	require.NoError(t, manager.SceneMetadataModelAssign(
		SceneMetadataModelRoleEntityExtraction,
		&working,
	))
	require.True(t, sceneMetadataEntityExtractorActive(working))

	// The replacement bundle passes checksum validation but cannot open an
	// ONNX session, which is how an out-of-memory activation presents.
	loader := loadSceneMetadataEntityExtractor
	loadSceneMetadataEntityExtractor = func(libraryPath, bundlePath string, threshold float64) (sceneMetadataExtractorSession, error) {
		if filepath.Base(bundlePath) == replacement {
			return nil, errors.New("cannot allocate ONNX session")
		}
		return loader(libraryPath, bundlePath, threshold)
	}

	err := manager.SceneMetadataModelAssign(
		SceneMetadataModelRoleEntityExtraction,
		&replacement,
	)

	require.Error(t, err)
	assert.Equal(t, working,
		cfg.GetSceneMetadataEntityModelAssignments()[entity.RoleEntityExtraction],
		"a failed activation must not persist the new assignment")
	assert.True(t, sceneMetadataEntityExtractorActive(working),
		"a failed activation must not unload the working model")
	assert.False(t, sessions[working].isClosed())
}

// useConcurrencySafeSceneMetadataLoader replaces the shared fake loader with
// one that does not write to an unsynchronised map, so concurrent lazy loads
// are safe to exercise.
func useConcurrencySafeSceneMetadataLoader(t *testing.T) {
	t.Helper()
	var mu sync.Mutex
	sessions := make(map[string]*fakeSceneMetadataSession)
	loadSceneMetadataEntityExtractor = func(_, bundlePath string, _ float64) (sceneMetadataExtractorSession, error) {
		key := filepath.Base(bundlePath)
		mu.Lock()
		defer mu.Unlock()
		session := &fakeSceneMetadataSession{key: key}
		sessions[key] = session
		return session, nil
	}
}

func TestAssignmentRemainsActiveUnderConcurrentLookups(t *testing.T) {
	cfg, _ := setupFakeSceneMetadataSessions(t)
	useConcurrencySafeSceneMetadataLoader(t)
	cfg.SetConfigFile(filepath.Join(t.TempDir(), "config.yml"))
	cachePath := cfg.GetCachePath()
	working := installFakeSceneMetadataBundle(t, cachePath, 0)
	replacement := installFakeSceneMetadataBundle(t, cachePath, 1)
	manager := &Manager{Config: cfg}

	require.NoError(t, manager.SceneMetadataModelAssign(
		SceneMetadataModelRoleEntityExtraction,
		&working,
	))

	// Extractor lookups prune sessions against the assignments they observe. A
	// lookup that overlaps the switch must not be able to close the session the
	// assignment just installed. The window is the config commit itself, so
	// pad the config to make each write slow and take many swings at it.
	cfg.SetString(config.ScraperUserAgent, strings.Repeat("x", 128<<10))

	stop := make(chan struct{})
	var lookups sync.WaitGroup
	for range 4 {
		lookups.Add(1)
		go func() {
			defer lookups.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = getSceneMetadataEntityExtractor()
			}
		}()
	}
	defer func() {
		close(stop)
		lookups.Wait()
	}()

	for round := range 10 {
		for _, target := range []string{replacement, working} {
			require.NoError(t, manager.SceneMetadataModelAssign(
				SceneMetadataModelRoleEntityExtraction,
				&target,
			), "round %d", round)
			require.Equal(t, target,
				cfg.GetSceneMetadataEntityModelAssignments()[entity.RoleEntityExtraction],
				"round %d", round)
			require.True(t, sceneMetadataEntityExtractorActive(target),
				"round %d: a successful assignment must leave the configured model active", round)
		}
	}
}

func TestConcurrentRoleAssignmentsDoNotClobber(t *testing.T) {
	cfg, _ := setupFakeSceneMetadataSessions(t)
	useConcurrencySafeSceneMetadataLoader(t)
	cfg.SetConfigFile(filepath.Join(t.TempDir(), "config.yml"))
	cachePath := cfg.GetCachePath()
	installFakeSceneMetadataBundle(t, cachePath, 0)
	entityModel := installFakeSceneMetadataBundle(t, cachePath, 1)
	contextModel := installFakeSceneMetadataBundle(t, cachePath, 2)
	manager := &Manager{Config: cfg}

	// Disjoint roles with disjoint model keys share no per-model lock, so only
	// a global transaction keeps the two read-modify-write cycles from
	// reverting each other.
	var assignments sync.WaitGroup
	var entityErr, contextErr error
	assignments.Add(2)
	go func() {
		defer assignments.Done()
		entityErr = manager.SceneMetadataModelAssign(
			SceneMetadataModelRoleEntityExtraction, &entityModel)
	}()
	go func() {
		defer assignments.Done()
		contextErr = manager.SceneMetadataModelAssign(
			SceneMetadataModelRolePerformerContext, &contextModel)
	}()
	assignments.Wait()

	require.NoError(t, entityErr)
	require.NoError(t, contextErr)
	final := cfg.GetSceneMetadataEntityModelAssignments()
	assert.Equal(t, entityModel, final[entity.RoleEntityExtraction],
		"the performer-context assignment must not revert the entity role")
	assert.Equal(t, contextModel, final[entity.RolePerformerContext],
		"the entity assignment must not revert the performer-context role")
}

func TestAssignmentHoldsModelLockAcrossReadinessCheck(t *testing.T) {
	cfg, _ := setupFakeSceneMetadataSessions(t)
	cfg.SetConfigFile(filepath.Join(t.TempDir(), "config.yml"))
	cachePath := cfg.GetCachePath()
	working := installFakeSceneMetadataBundle(t, cachePath, 0)
	target := installFakeSceneMetadataBundle(t, cachePath, 1)
	manager := &Manager{Config: cfg}

	// Stand in for a concurrent uninstall: hold the target's install lock, then
	// remove its bundle before releasing.
	release := lockSceneMetadataModelKeys(target)
	assigned := make(chan error, 1)
	go func() {
		assigned <- manager.SceneMetadataModelAssign(
			SceneMetadataModelRoleEntityExtraction,
			&target,
		)
	}()

	select {
	case err := <-assigned:
		t.Fatalf("assignment checked readiness outside the model lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, os.RemoveAll(entity.BundlePath(cachePath, target)))
	release()

	require.Error(t, <-assigned)
	assert.Equal(t, working,
		cfg.GetSceneMetadataEntityModelAssignments()[entity.RoleEntityExtraction],
		"a model uninstalled during assignment must not become active")
}
