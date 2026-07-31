package manager

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindOnnxRuntimeLibrary(t *testing.T) {
	dir := t.TempDir()
	fakeLib := filepath.Join(dir, "libonnxruntime.so")
	if err := os.WriteFile(fakeLib, []byte{}, 0o644); err != nil {
		t.Fatalf("writing fake library: %v", err)
	}

	original := onnxRuntimeLibraryPaths
	t.Cleanup(func() { onnxRuntimeLibraryPaths = original })

	t.Run("finds an existing path", func(t *testing.T) {
		onnxRuntimeLibraryPaths = []string{"", "/does/not/exist", fakeLib}
		path, ok := findOnnxRuntimeLibrary()
		if !ok {
			t.Fatal("expected to find the fake library, got ok=false")
		}
		if path != fakeLib {
			t.Errorf("path = %q, want %q", path, fakeLib)
		}
	})

	t.Run("returns false when nothing exists", func(t *testing.T) {
		onnxRuntimeLibraryPaths = []string{"", "/does/not/exist", "/also/not/here"}
		_, ok := findOnnxRuntimeLibrary()
		if ok {
			t.Error("expected ok=false when no candidate path exists")
		}
	})
}

func resetSceneMetadataEntityState(t *testing.T) {
	t.Helper()
	closeSceneMetadataEntityExtractor()
}

func TestReloadSceneMetadataEntityExtractorWithoutRuntime(t *testing.T) {
	originalPaths := onnxRuntimeLibraryPaths
	onnxRuntimeLibraryPaths = []string{"/does/not/exist"}
	t.Setenv("STASH_ONNXRUNTIME_LIB_PATH", "")

	resetSceneMetadataEntityState(t)
	t.Cleanup(func() {
		onnxRuntimeLibraryPaths = originalPaths
		resetSceneMetadataEntityState(t)
	})

	if extractor := getSceneMetadataEntityExtractor(); extractor != nil {
		t.Fatalf("extractor = %T, want nil deterministic fallback", extractor)
	}
	_ = reloadSceneMetadataEntityExtractor("gliner-small-v2.1-int8")
	_ = reloadSceneMetadataEntityExtractor("gliner-small-v2.1-int8")
	if extractor := getSceneMetadataEntityExtractor(); extractor != nil {
		t.Fatalf("extractor = %T, want nil deterministic fallback", extractor)
	}
	if _, ok := getNamePlausibilityScorer().(metadata.HeuristicNamePlausibilityScorer); !ok {
		t.Fatal("heuristic plausibility guard was not retained")
	}
}

type fakeSceneMetadataSession struct {
	key    string
	mu     sync.Mutex
	closed bool
}

func (s *fakeSceneMetadataSession) Extract(context.Context, string) ([]metadata.EntitySpan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []metadata.EntitySpan{{Label: s.key}}, nil
}

func (s *fakeSceneMetadataSession) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *fakeSceneMetadataSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func setupFakeSceneMetadataSessions(t *testing.T) (*config.Config, map[string]*fakeSceneMetadataSession) {
	t.Helper()
	resetSceneMetadataEntityState(t)
	previousInstance := instance
	previousLoader := loadSceneMetadataEntityExtractor
	previousPaths := onnxRuntimeLibraryPaths

	cachePath := t.TempDir()
	cfg := config.InitializeEmpty()
	cfg.SetString(config.Cache, cachePath)
	instance = &Manager{Config: cfg}
	fakeLibrary := filepath.Join(cachePath, "libonnxruntime.so")
	require.NoError(t, os.WriteFile(fakeLibrary, nil, 0o600))
	onnxRuntimeLibraryPaths = []string{fakeLibrary}
	t.Setenv("STASH_ONNXRUNTIME_LIB_PATH", "")

	sessions := make(map[string]*fakeSceneMetadataSession)
	loadSceneMetadataEntityExtractor = func(_, bundlePath string, _ float64) (sceneMetadataExtractorSession, error) {
		key := filepath.Base(bundlePath)
		session := &fakeSceneMetadataSession{key: key}
		sessions[key] = session
		return session, nil
	}
	t.Cleanup(func() {
		resetSceneMetadataEntityState(t)
		instance = previousInstance
		loadSceneMetadataEntityExtractor = previousLoader
		onnxRuntimeLibraryPaths = previousPaths
	})
	return cfg, sessions
}

func TestReloadSceneMetadataEntityExtractorPerKey(t *testing.T) {
	cfg, sessions := setupFakeSceneMetadataSessions(t)
	const (
		keyA = "gliner-small-v2.1-int8"
		keyB = "gliner-medium-v2.1-int8"
	)
	assignments := map[entity.Role]string{
		entity.RoleEntityExtraction: keyA,
		entity.RolePerformerContext: keyB,
	}
	require.NoError(t, cfg.SetSceneMetadataEntityModelAssignments(assignments))
	for _, key := range []string{keyA, keyB} {
		require.NoError(t, os.MkdirAll(entity.BundlePath(cfg.GetCachePath(), key), 0o755))
	}

	syncSceneMetadataEntitySessions(assignments)
	require.NoError(t, reloadSceneMetadataEntityExtractor(keyA))
	require.NoError(t, reloadSceneMetadataEntityExtractor(keyB))
	assert.True(t, sceneMetadataEntityExtractorActive(keyA))
	assert.True(t, sceneMetadataEntityExtractorActive(keyB))

	syncSceneMetadataEntitySessions(map[entity.Role]string{entity.RoleEntityExtraction: keyB})
	assert.True(t, sessions[keyA].isClosed())
	assert.False(t, sessions[keyB].isClosed())
	assert.False(t, sceneMetadataEntityExtractorActive(keyA))
	assert.True(t, sceneMetadataEntityExtractorActive(keyB))
	assert.DirExists(t, entity.BundlePath(cfg.GetCachePath(), keyA))
	assert.DirExists(t, entity.BundlePath(cfg.GetCachePath(), keyB))
}

func TestResolveEntityExtractorFollowsAssignment(t *testing.T) {
	cfg, sessions := setupFakeSceneMetadataSessions(t)
	const (
		keyA = "gliner-small-v2.1-int8"
		keyB = "gliner-large-v2.1-int8"
	)
	require.NoError(t, cfg.SetSceneMetadataEntityModelAssignments(map[entity.Role]string{
		entity.RoleEntityExtraction: keyA,
	}))
	extractorA := getSceneMetadataEntityExtractorForRole(entity.RoleEntityExtraction)
	require.NotNil(t, extractorA)
	spans, err := extractorA.Extract(context.Background(), "input")
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.Equal(t, keyA, spans[0].Label)

	require.NoError(t, cfg.SetSceneMetadataEntityModelAssignments(map[entity.Role]string{
		entity.RoleEntityExtraction: keyB,
	}))
	extractorB := getSceneMetadataEntityExtractorForRole(entity.RoleEntityExtraction)
	require.NotNil(t, extractorB)
	spans, err = extractorB.Extract(context.Background(), "input")
	require.NoError(t, err)
	require.Len(t, spans, 1)
	assert.Equal(t, keyB, spans[0].Label)
	assert.True(t, sessions[keyA].isClosed())
	assert.False(t, sessions[keyB].isClosed())
}
