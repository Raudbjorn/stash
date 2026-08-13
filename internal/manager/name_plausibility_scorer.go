package manager

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
)

var onnxRuntimeLibraryPaths = []string{
	"/usr/lib/libonnxruntime.so",
	"/usr/lib/libonnxruntime.so.1",
	"/usr/local/lib/libonnxruntime.so",
}

type sceneMetadataExtractorSession interface {
	metadata.EntityExtractor
	Close() error
}

type keyedMutexes struct {
	mu     sync.Mutex
	values map[string]*sync.Mutex
}

func (m *keyedMutexes) lock(key string) func() {
	m.mu.Lock()
	if m.values == nil {
		m.values = make(map[string]*sync.Mutex)
	}
	mutex := m.values[key]
	if mutex == nil {
		mutex = &sync.Mutex{}
		m.values[key] = mutex
	}
	m.mu.Unlock()
	mutex.Lock()
	return mutex.Unlock
}

var loadSceneMetadataEntityExtractor = func(libraryPath, bundlePath string, threshold float64) (sceneMetadataExtractorSession, error) {
	return entity.Load(libraryPath, bundlePath, threshold)
}

var (
	// sceneMetadataEntitySwapMu serialises the whole assignment
	// read-modify-write transaction with the session pruning that follows it.
	// Per-model install locks cannot do this on their own: two assignments to
	// different roles with disjoint model keys would otherwise read the same
	// assignment map and the later write would revert the earlier role, and a
	// prune running from a stale snapshot could close a session another
	// assignment had just published.
	//
	// Lock order is sceneMetadataEntitySwapMu -> sceneMetadataEntityInstallMu
	// -> sceneMetadataEntityReloadMu -> sceneMetadataEntityMu. The install job
	// deliberately stays outside the swap lock so a long download cannot block
	// assignments; it only ever reloads a key that is already assigned.
	sceneMetadataEntitySwapMu   sync.Mutex
	sceneMetadataEntityReloadMu keyedMutexes

	sceneMetadataEntityMu            sync.RWMutex
	sceneMetadataEntityExtractor     = make(map[string]sceneMetadataExtractorSession)
	sceneMetadataEntityLoaded        = make(map[string]bool)
	sceneMetadataEntitySessionCounts = make(map[string]int)
	sceneMetadataEntityLastError     = make(map[string]string)
)

// The deterministic scorer remains the cheap structural guard for heuristic
// two-word candidates. Learned extraction is a separate typed evidence path.
func getNamePlausibilityScorer() metadata.NamePlausibilityScorer {
	return metadata.HeuristicNamePlausibilityScorer{}
}

type managedSceneMetadataExtractor struct {
	modelKey string
}

func (e managedSceneMetadataExtractor) Extract(ctx context.Context, text string) ([]metadata.EntitySpan, error) {
	sceneMetadataEntityMu.RLock()
	defer sceneMetadataEntityMu.RUnlock()
	extractor := sceneMetadataEntityExtractor[e.modelKey]
	if extractor == nil {
		return nil, entity.ErrBundleMissing
	}
	return extractor.Extract(ctx, text)
}

func currentSceneMetadataModelAssignments() map[entity.Role]string {
	if instance == nil || instance.Config == nil {
		return map[entity.Role]string{
			entity.RoleEntityExtraction: "gliner-small-v2.1-int8",
		}
	}
	return instance.Config.GetSceneMetadataEntityModelAssignments()
}

// pruneSceneMetadataEntitySessions closes every session that no longer backs an
// assigned role. Callers must hold sceneMetadataEntitySwapMu, so a prune can
// never run from a stale snapshot alongside a half-finished assignment.
func pruneSceneMetadataEntitySessions(assignments map[entity.Role]string) {
	counts := make(map[string]int, len(assignments))
	for _, key := range assignments {
		if key != "" {
			counts[key]++
		}
	}

	var stale []sceneMetadataExtractorSession
	sceneMetadataEntityMu.Lock()
	for key, extractor := range sceneMetadataEntityExtractor {
		if counts[key] == 0 {
			stale = append(stale, extractor)
			delete(sceneMetadataEntityExtractor, key)
			delete(sceneMetadataEntityLoaded, key)
			delete(sceneMetadataEntityLastError, key)
		}
	}
	sceneMetadataEntitySessionCounts = counts
	sceneMetadataEntityMu.Unlock()
	for _, extractor := range stale {
		if err := extractor.Close(); err != nil {
			logger.Warnf("[scene metadata] closing unassigned GLiNER session: %v", err)
		}
	}
}

func unloadSceneMetadataEntityExtractor(modelKey string) {
	sceneMetadataEntityMu.Lock()
	extractor := sceneMetadataEntityExtractor[modelKey]
	delete(sceneMetadataEntityExtractor, modelKey)
	delete(sceneMetadataEntityLoaded, modelKey)
	delete(sceneMetadataEntitySessionCounts, modelKey)
	delete(sceneMetadataEntityLastError, modelKey)
	sceneMetadataEntityMu.Unlock()
	if extractor != nil {
		if err := extractor.Close(); err != nil {
			logger.Warnf("[scene metadata] closing GLiNER session %s: %v", modelKey, err)
		}
	}
}

func getSceneMetadataEntityExtractorForRole(role entity.Role) metadata.EntityExtractor {
	// Resolving the assignment, pruning, and any lazy load all happen under the
	// swap lock, so a lookup can never prune a session that an in-flight
	// assignment has already published.
	sceneMetadataEntitySwapMu.Lock()
	defer sceneMetadataEntitySwapMu.Unlock()

	assignments := currentSceneMetadataModelAssignments()
	pruneSceneMetadataEntitySessions(assignments)
	modelKey := assignments[role]
	if modelKey == "" {
		return nil
	}

	unlock := sceneMetadataEntityReloadMu.lock(modelKey)
	sceneMetadataEntityMu.RLock()
	loaded := sceneMetadataEntityLoaded[modelKey]
	sceneMetadataEntityMu.RUnlock()
	if !loaded {
		_ = doReloadSceneMetadataEntityExtractor(modelKey)
		sceneMetadataEntityMu.Lock()
		sceneMetadataEntityLoaded[modelKey] = true
		sceneMetadataEntityMu.Unlock()
	}
	unlock()

	if !sceneMetadataEntityExtractorActive(modelKey) {
		return nil
	}
	return managedSceneMetadataExtractor{modelKey: modelKey}
}

func getSceneMetadataEntityExtractor() metadata.EntityExtractor {
	return getSceneMetadataEntityExtractorForRole(entity.RoleEntityExtraction)
}

func sceneMetadataEntityExtractorActive(modelKey string) bool {
	sceneMetadataEntityMu.RLock()
	defer sceneMetadataEntityMu.RUnlock()
	return sceneMetadataEntityExtractor[modelKey] != nil
}

func sceneMetadataEntityRuntimeError(modelKey string) string {
	sceneMetadataEntityMu.RLock()
	defer sceneMetadataEntityMu.RUnlock()
	return sceneMetadataEntityLastError[modelKey]
}

func reloadSceneMetadataEntityExtractor(modelKey string) error {
	unlock := sceneMetadataEntityReloadMu.lock(modelKey)
	defer unlock()
	err := doReloadSceneMetadataEntityExtractor(modelKey)
	sceneMetadataEntityMu.Lock()
	sceneMetadataEntityLoaded[modelKey] = true
	sceneMetadataEntityMu.Unlock()
	return err
}

// openSceneMetadataEntitySession loads a bundle without publishing it, so an
// assignment can prove a replacement works before committing to it. An
// unpublished session is invisible to pruning and cannot be closed by anyone
// else; the caller owns it until it is published or closed.
func openSceneMetadataEntitySession(modelKey string) (sceneMetadataExtractorSession, error) {
	recordFailure := func(err error) error {
		sceneMetadataEntityMu.Lock()
		sceneMetadataEntityLastError[modelKey] = err.Error()
		sceneMetadataEntityMu.Unlock()
		return err
	}

	libPath, ok := findOnnxRuntimeLibrary()
	if !ok {
		err := recordFailure(fmt.Errorf("onnxruntime shared library not found"))
		logger.Infof("[scene metadata] %v; deterministic analysis remains available", err)
		return nil, err
	}
	if instance == nil || instance.Config == nil {
		err := recordFailure(fmt.Errorf("configuration unavailable"))
		logger.Infof("[scene metadata] %v; deterministic analysis remains available", err)
		return nil, err
	}
	bundlePath := entity.BundlePath(instance.Config.GetCachePath(), modelKey)
	session, err := loadSceneMetadataEntityExtractor(libPath, bundlePath, entity.DefaultThreshold)
	if err != nil {
		err = recordFailure(err)
		logger.Infof("[scene metadata] GLiNER %s unavailable: %v; preserving the current session", modelKey, err)
		return nil, err
	}
	return session, nil
}

// publishSceneMetadataEntitySession installs an already-opened session and
// closes whatever it replaces. The shared entity runtime keeps one ref-counted
// ONNX environment, so the old session remains usable until all in-flight
// calls finish and Close runs after the swap.
func publishSceneMetadataEntitySession(modelKey string, session sceneMetadataExtractorSession) {
	sceneMetadataEntityMu.Lock()
	old := sceneMetadataEntityExtractor[modelKey]
	sceneMetadataEntityExtractor[modelKey] = session
	sceneMetadataEntityLoaded[modelKey] = true
	delete(sceneMetadataEntityLastError, modelKey)
	sceneMetadataEntityMu.Unlock()
	if old != nil && old != session {
		if err := old.Close(); err != nil {
			logger.Warnf("[scene metadata] closing previous GLiNER session: %v", err)
		}
	}
	logger.Infof("[scene metadata] using local %s entity model", modelKey)
}

// closeSceneMetadataEntitySession discards an opened but unpublished session.
func closeSceneMetadataEntitySession(session sceneMetadataExtractorSession) {
	if session == nil {
		return
	}
	if err := session.Close(); err != nil {
		logger.Warnf("[scene metadata] closing unpublished GLiNER session: %v", err)
	}
}

func doReloadSceneMetadataEntityExtractor(modelKey string) error {
	session, err := openSceneMetadataEntitySession(modelKey)
	if err != nil {
		return err
	}
	publishSceneMetadataEntitySession(modelKey, session)
	return nil
}

func closeSceneMetadataEntityExtractor() {
	sceneMetadataEntityMu.Lock()
	old := sceneMetadataEntityExtractor
	sceneMetadataEntityExtractor = make(map[string]sceneMetadataExtractorSession)
	sceneMetadataEntityLoaded = make(map[string]bool)
	sceneMetadataEntitySessionCounts = make(map[string]int)
	sceneMetadataEntityLastError = make(map[string]string)
	sceneMetadataEntityMu.Unlock()
	for _, extractor := range old {
		if err := extractor.Close(); err != nil {
			logger.Warnf("[scene metadata] closing GLiNER session: %v", err)
		}
	}
}

func findOnnxRuntimeLibrary() (string, bool) {
	var configuredPath string
	if instance != nil && instance.Config != nil {
		configuredPath = instance.Config.GetOnnxRuntimeLibPath()
	}

	candidates := make([]string, 0, len(onnxRuntimeLibraryPaths)+2)
	candidates = append(candidates, configuredPath, os.Getenv("STASH_ONNXRUNTIME_LIB_PATH"))
	candidates = append(candidates, onnxRuntimeLibraryPaths...)
	for _, path := range candidates {
		if path == "" {
			continue
		}
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}
