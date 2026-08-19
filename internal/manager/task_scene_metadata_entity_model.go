package manager

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/scene/metadata/entity"
)

type SceneMetadataModelRole string

const (
	SceneMetadataModelRoleEntityExtraction        SceneMetadataModelRole = "ENTITY_EXTRACTION"
	SceneMetadataModelRolePerformerContext        SceneMetadataModelRole = "PERFORMER_CONTEXT"
	SceneMetadataModelRoleStudioProviderSelection SceneMetadataModelRole = "STUDIO_PROVIDER_SELECTION"
)

func (r SceneMetadataModelRole) IsValid() bool {
	switch r {
	case SceneMetadataModelRoleEntityExtraction,
		SceneMetadataModelRolePerformerContext,
		SceneMetadataModelRoleStudioProviderSelection:
		return true
	default:
		return false
	}
}

func (r SceneMetadataModelRole) String() string {
	return string(r)
}

func (r *SceneMetadataModelRole) UnmarshalGQL(value interface{}) error {
	raw, ok := value.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}
	*r = SceneMetadataModelRole(raw)
	if !r.IsValid() {
		return fmt.Errorf("%s is not a valid SceneMetadataModelRole", raw)
	}
	return nil
}

func (r SceneMetadataModelRole) MarshalGQL(writer io.Writer) {
	fmt.Fprint(writer, strconv.Quote(r.String()))
}

func (r SceneMetadataModelRole) entityRole() entity.Role {
	switch r {
	case SceneMetadataModelRoleEntityExtraction:
		return entity.RoleEntityExtraction
	case SceneMetadataModelRolePerformerContext:
		return entity.RolePerformerContext
	case SceneMetadataModelRoleStudioProviderSelection:
		return entity.RoleStudioProviderSelection
	default:
		return ""
	}
}

func sceneMetadataModelRole(role entity.Role) SceneMetadataModelRole {
	switch role {
	case entity.RoleEntityExtraction:
		return SceneMetadataModelRoleEntityExtraction
	case entity.RolePerformerContext:
		return SceneMetadataModelRolePerformerContext
	case entity.RoleStudioProviderSelection:
		return SceneMetadataModelRoleStudioProviderSelection
	default:
		return ""
	}
}

type SceneMetadataModel struct {
	Key         string
	DisplayName string
	Family      string
	Precision   string
	Size        int
	License     string
	LicenseURL  string
	Installed   bool
	Present     bool
	Validated   bool
	Active      bool
	LastError   *string
}

type SceneMetadataModelAssignment struct {
	Role     SceneMetadataModelRole
	ModelKey *string
}

type SceneMetadataModelStatus struct {
	ActiveKey        string
	State            string
	CachePath        string
	RuntimeAvailable bool
	LastError        string
}

func (s *Manager) SceneMetadataModels() []SceneMetadataModel {
	cachePath := s.Config.GetCachePath()
	activeKey := s.Config.GetSceneMetadataEntityModelAssignments()[entity.RoleEntityExtraction]
	installer := entity.Installer{}
	if libraryPath, ok := findOnnxRuntimeLibrary(); ok {
		installer.ValidateModel = func(modelPath string) error {
			return entity.ValidateModelSignature(libraryPath, modelPath)
		}
	}
	result := make([]SceneMetadataModel, 0, len(entity.Catalog))
	for _, spec := range entity.Catalog {
		state, validationErr := entity.InspectModelState(cachePath, spec.Key, activeKey, installer)
		status := entity.Status(cachePath, spec.Key)
		lastError := status.LastError
		if validationErr != nil {
			lastError = validationErr.Error()
		}
		if runtimeError := sceneMetadataEntityRuntimeError(spec.Key); runtimeError != "" {
			lastError = runtimeError
		}
		var lastErrorPointer *string
		if lastError != "" && (status.State != entity.ModelMissing || state.Present) {
			lastErrorPointer = &lastError
		}
		result = append(result, SceneMetadataModel{
			Key: spec.Key, DisplayName: spec.Display, Family: spec.Family,
			Precision: spec.Precision, Size: int(spec.ApproxSize),
			License: spec.License, LicenseURL: spec.LicenseURL,
			Installed: state.Present && state.Validated && state.Active,
			Present:   state.Present, Validated: state.Validated, Active: state.Active,
			LastError: lastErrorPointer,
		})
	}
	return result
}

func (s *Manager) SceneMetadataModelAssignments() []SceneMetadataModelAssignment {
	assignments := s.Config.GetSceneMetadataEntityModelAssignments()
	roles := []entity.Role{
		entity.RoleEntityExtraction,
		entity.RolePerformerContext,
		entity.RoleStudioProviderSelection,
	}
	result := make([]SceneMetadataModelAssignment, 0, len(roles))
	for _, role := range roles {
		var modelKey *string
		if key := assignments[role]; key != "" {
			keyCopy := key
			modelKey = &keyCopy
		}
		result = append(result, SceneMetadataModelAssignment{
			Role: sceneMetadataModelRole(role), ModelKey: modelKey,
		})
	}
	return result
}

func (s *Manager) SceneMetadataModelStatus() SceneMetadataModelStatus {
	assignments := s.Config.GetSceneMetadataEntityModelAssignments()
	activeKey := assignments[entity.RoleEntityExtraction]
	_, runtimeAvailable := findOnnxRuntimeLibrary()
	if activeKey == "" {
		return SceneMetadataModelStatus{
			ActiveKey: "", State: string(entity.ModelMissing),
			CachePath:        entity.ModelRootPath(s.Config.GetCachePath()),
			RuntimeAvailable: runtimeAvailable,
		}
	}
	status := entity.Status(s.Config.GetCachePath(), activeKey)
	lastError := status.LastError
	if runtimeError := sceneMetadataEntityRuntimeError(activeKey); runtimeError != "" {
		lastError = runtimeError
	}
	return SceneMetadataModelStatus{
		ActiveKey: activeKey, State: string(status.State), CachePath: status.CachePath,
		RuntimeAvailable: runtimeAvailable, LastError: lastError,
	}
}

func (s *Manager) SceneMetadataModelInstall(ctx context.Context, modelKey string) int {
	return s.JobManager.Add(ctx, "Installing scene metadata model...", &installSceneMetadataEntityModelJob{
		cachePath: s.Config.GetCachePath(),
		modelKey:  modelKey,
	})
}

func (s *Manager) SceneMetadataModelUninstall(modelKey string) error {
	if _, ok := entity.FindModel(modelKey); !ok {
		return fmt.Errorf("unknown scene metadata model key %q", modelKey)
	}
	// The swap lock keeps the "is it assigned" check and the removal in the same
	// transaction as any concurrent assignment.
	sceneMetadataEntitySwapMu.Lock()
	defer sceneMetadataEntitySwapMu.Unlock()
	unlock := sceneMetadataEntityInstallMu.lock(modelKey)
	defer unlock()
	for role, assignedKey := range s.Config.GetSceneMetadataEntityModelAssignments() {
		if assignedKey == modelKey {
			return fmt.Errorf("model is assigned to role %s", role)
		}
	}
	unloadSceneMetadataEntityExtractor(modelKey)
	if err := os.RemoveAll(entity.BundlePath(s.Config.GetCachePath(), modelKey)); err != nil {
		return fmt.Errorf("remove scene metadata model %q: %w", modelKey, err)
	}
	return nil
}

// SceneMetadataModelRepair removes a present bundle only when validation
// proves it is unusable. A valid bundle is never deleted.
func (s *Manager) SceneMetadataModelRepair(modelKey string) (bool, error) {
	if _, ok := entity.FindModel(modelKey); !ok {
		return false, fmt.Errorf("unknown scene metadata model key %q", modelKey)
	}
	sceneMetadataEntitySwapMu.Lock()
	defer sceneMetadataEntitySwapMu.Unlock()
	unlock := sceneMetadataEntityInstallMu.lock(modelKey)
	defer unlock()

	installer := entity.Installer{}
	if libraryPath, ok := findOnnxRuntimeLibrary(); ok {
		installer.ValidateModel = func(modelPath string) error {
			return entity.ValidateModelSignature(libraryPath, modelPath)
		}
	}
	state, validationErr := entity.InspectModelState(
		s.Config.GetCachePath(),
		modelKey,
		s.Config.GetSceneMetadataEntityModelAssignments()[entity.RoleEntityExtraction],
		installer,
	)
	if !state.Present || validationErr == nil {
		return false, nil
	}
	unloadSceneMetadataEntityExtractor(modelKey)
	if err := os.RemoveAll(entity.BundlePath(s.Config.GetCachePath(), modelKey)); err != nil {
		return false, fmt.Errorf("repair scene metadata model %q: %w", modelKey, err)
	}
	return true, nil
}

// lockSceneMetadataModelKeys takes the install lock for every distinct key in
// a deterministic order, so two assignments that touch the same pair of models
// from opposite directions cannot deadlock.
func lockSceneMetadataModelKeys(keys ...string) func() {
	distinct := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		distinct = append(distinct, key)
	}
	sort.Strings(distinct)

	unlocks := make([]func(), 0, len(distinct))
	for _, key := range distinct {
		unlocks = append(unlocks, sceneMetadataEntityInstallMu.lock(key))
	}
	return func() {
		for index := len(unlocks) - 1; index >= 0; index-- {
			unlocks[index]()
		}
	}
}

func (s *Manager) SceneMetadataModelAssign(role SceneMetadataModelRole, modelKey *string) error {
	entityRole := role.entityRole()
	if entityRole == "" {
		return fmt.Errorf("unknown scene metadata model role %q", role)
	}
	var requested string
	if modelKey != nil {
		requested = *modelKey
	}
	if requested != "" {
		if _, ok := entity.FindModel(requested); !ok {
			return fmt.Errorf("unknown scene metadata model key %q", requested)
		}
	}

	// Serialise the entire read-modify-write against every other assignment and
	// against session pruning. The per-model install locks below only cover
	// operations that share a model key, so two roles with disjoint keys would
	// otherwise both read this map and the later write would revert the other
	// role.
	sceneMetadataEntitySwapMu.Lock()
	defer sceneMetadataEntitySwapMu.Unlock()

	previous := s.Config.GetSceneMetadataEntityModelAssignments()
	// Hold the install lock for both the outgoing and the incoming model across
	// the whole transaction. A concurrent uninstall must not be able to remove
	// the bundle between the readiness check and the assignment being written.
	unlock := lockSceneMetadataModelKeys(previous[entityRole], requested)
	defer unlock()

	if requested != "" &&
		entity.Status(s.Config.GetCachePath(), requested).State != entity.ModelReady {
		return fmt.Errorf("scene metadata model %q is not installed", requested)
	}

	// Prove the replacement works before anything is committed and before the
	// outgoing session is released. A bundle can pass checksum validation and
	// still fail to open an ONNX session, and the working model has to survive
	// that. The session stays private until the commit succeeds so nothing can
	// prune it mid-transaction.
	var replacement sceneMetadataExtractorSession
	if entityRole == entity.RoleEntityExtraction && requested != "" {
		session, err := openSceneMetadataEntitySession(requested)
		if err != nil {
			return fmt.Errorf("activate scene metadata model %q: %w", requested, err)
		}
		replacement = session
	}

	assignments := make(map[entity.Role]string, len(previous))
	for assignedRole, assignedKey := range previous {
		assignments[assignedRole] = assignedKey
	}
	assignments[entityRole] = requested
	if err := s.Config.SetSceneMetadataEntityModelAssignments(assignments); err != nil {
		closeSceneMetadataEntitySession(replacement)
		return err
	}
	previousSelected := s.Config.GetSceneMetadataEntityModel()
	if entityRole == entity.RoleEntityExtraction {
		s.Config.SetSceneMetadataEntityModel(requested)
	}
	if err := s.Config.Write(); err != nil {
		_ = s.Config.SetSceneMetadataEntityModelAssignments(previous)
		s.Config.SetSceneMetadataEntityModel(previousSelected)
		closeSceneMetadataEntitySession(replacement)
		return fmt.Errorf("write scene metadata model assignment: %w", err)
	}

	// The assignment is durable. Publish the replacement, then release whatever
	// the new assignment no longer references.
	if replacement != nil {
		publishSceneMetadataEntitySession(requested, replacement)
	}
	pruneSceneMetadataEntitySessions(assignments)
	return nil
}

func (s *Manager) SceneMetadataModelReload() bool {
	sceneMetadataEntitySwapMu.Lock()
	defer sceneMetadataEntitySwapMu.Unlock()

	assignments := s.Config.GetSceneMetadataEntityModelAssignments()
	activeKey := assignments[entity.RoleEntityExtraction]
	if activeKey == "" {
		return false
	}
	pruneSceneMetadataEntitySessions(assignments)
	_ = doReloadSceneMetadataEntityExtractor(activeKey)
	return sceneMetadataEntityExtractorActive(activeKey)
}

var sceneMetadataEntityInstallMu keyedMutexes

type installSceneMetadataEntityModelJob struct {
	cachePath string
	modelKey  string
}

func (j *installSceneMetadataEntityModelJob) Execute(ctx context.Context, progress *job.Progress) error {
	unlock := sceneMetadataEntityInstallMu.lock(j.modelKey)
	defer unlock()

	if _, ok := entity.FindModel(j.modelKey); !ok {
		return fmt.Errorf("unknown scene metadata model key %q", j.modelKey)
	}
	libraryPath, ok := findOnnxRuntimeLibrary()
	if !ok {
		return fmt.Errorf("ONNX Runtime is required to validate the scene metadata entity model")
	}
	installer := entity.Installer{ValidateModel: func(modelPath string) error {
		return entity.ValidateModelSignature(libraryPath, modelPath)
	}}
	if err := installer.Install(ctx, j.cachePath, j.modelKey, func(completed, total int64) {
		if total > 0 {
			progress.SetPercent(float64(completed) / float64(total))
		}
	}); err != nil {
		return err
	}
	assignments := currentSceneMetadataModelAssignments()
	if assignments[entity.RoleEntityExtraction] == j.modelKey {
		if err := reloadSceneMetadataEntityExtractor(j.modelKey); err != nil {
			return fmt.Errorf("scene metadata entity model installed but could not be activated: %w", err)
		}
	}
	progress.SetPercent(1)
	return nil
}
