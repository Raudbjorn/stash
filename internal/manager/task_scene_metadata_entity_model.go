package manager

import (
	"context"
	"fmt"
	"io"
	"os"
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
	result := make([]SceneMetadataModel, 0, len(entity.Catalog))
	for _, spec := range entity.Catalog {
		status := entity.Status(cachePath, spec.Key)
		_, statErr := os.Stat(entity.BundlePath(cachePath, spec.Key))
		installed := statErr == nil
		lastError := status.LastError
		if runtimeError := sceneMetadataEntityRuntimeError(spec.Key); runtimeError != "" {
			lastError = runtimeError
		}
		var lastErrorPointer *string
		if lastError != "" && (status.State != entity.ModelMissing || installed) {
			lastErrorPointer = &lastError
		}
		result = append(result, SceneMetadataModel{
			Key: spec.Key, DisplayName: spec.Display, Family: spec.Family,
			Precision: spec.Precision, Size: int(spec.ApproxSize),
			License: spec.License, LicenseURL: spec.LicenseURL,
			Installed: installed, LastError: lastErrorPointer,
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

func (s *Manager) SceneMetadataModelAssign(role SceneMetadataModelRole, modelKey *string) error {
	entityRole := role.entityRole()
	if entityRole == "" {
		return fmt.Errorf("unknown scene metadata model role %q", role)
	}
	previous := s.Config.GetSceneMetadataEntityModelAssignments()
	if modelKey != nil && *modelKey != "" {
		if _, ok := entity.FindModel(*modelKey); !ok {
			return fmt.Errorf("unknown scene metadata model key %q", *modelKey)
		}
		if entity.Status(s.Config.GetCachePath(), *modelKey).State != entity.ModelReady {
			return fmt.Errorf("scene metadata model %q is not installed", *modelKey)
		}
	}
	lockKey := previous[entityRole]
	if modelKey != nil && *modelKey != "" {
		lockKey = *modelKey
	}
	if lockKey != "" {
		unlock := sceneMetadataEntityInstallMu.lock(lockKey)
		defer unlock()
	}

	assignments := make(map[entity.Role]string, len(previous))
	for assignedRole, assignedKey := range previous {
		assignments[assignedRole] = assignedKey
	}
	if modelKey == nil || *modelKey == "" {
		assignments[entityRole] = ""
	} else {
		assignments[entityRole] = *modelKey
	}
	if err := s.Config.SetSceneMetadataEntityModelAssignments(assignments); err != nil {
		return err
	}
	previousSelected := s.Config.GetSceneMetadataEntityModel()
	if entityRole == entity.RoleEntityExtraction {
		if modelKey == nil {
			s.Config.SetSceneMetadataEntityModel("")
		} else {
			s.Config.SetSceneMetadataEntityModel(*modelKey)
		}
	}
	if err := s.Config.Write(); err != nil {
		_ = s.Config.SetSceneMetadataEntityModelAssignments(previous)
		s.Config.SetSceneMetadataEntityModel(previousSelected)
		return fmt.Errorf("write scene metadata model assignment: %w", err)
	}

	syncSceneMetadataEntitySessions(assignments)
	if entityRole == entity.RoleEntityExtraction && modelKey != nil &&
		entity.Status(s.Config.GetCachePath(), *modelKey).State == entity.ModelReady {
		_ = reloadSceneMetadataEntityExtractor(*modelKey)
	}
	return nil
}

func (s *Manager) SceneMetadataModelReload() bool {
	activeKey := s.Config.GetSceneMetadataEntityModelAssignments()[entity.RoleEntityExtraction]
	if activeKey == "" {
		return false
	}
	syncSceneMetadataEntitySessions(s.Config.GetSceneMetadataEntityModelAssignments())
	_ = reloadSceneMetadataEntityExtractor(activeKey)
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
