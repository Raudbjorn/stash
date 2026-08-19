package api

import (
	"context"

	"github.com/stashapp/stash/internal/manager"
)

func (r *queryResolver) SystemStatus(ctx context.Context) (*manager.SystemStatus, error) {
	return manager.GetInstance().GetSystemStatus(), nil
}
func (r *queryResolver) SceneMetadataModels(ctx context.Context) ([]*manager.SceneMetadataModel, error) {
	models := manager.GetInstance().SceneMetadataModels()
	result := make([]*manager.SceneMetadataModel, len(models))
	for index := range models {
		result[index] = &models[index]
	}
	return result, nil
}

func (r *queryResolver) SceneMetadataModelAssignments(ctx context.Context) ([]*manager.SceneMetadataModelAssignment, error) {
	assignments := manager.GetInstance().SceneMetadataModelAssignments()
	result := make([]*manager.SceneMetadataModelAssignment, len(assignments))
	for index := range assignments {
		result[index] = &assignments[index]
	}
	return result, nil
}

func (r *queryResolver) SceneMetadataModelStatus(ctx context.Context) (*manager.SceneMetadataModelStatus, error) {
	status := manager.GetInstance().SceneMetadataModelStatus()
	return &status, nil
}

func (r *queryResolver) SceneMetadataPlans(ctx context.Context, sceneIds []string, state *manager.SceneMetadataPlanState, latestOnly bool) ([]*manager.AnalysisPlan, error) {
	return manager.GetInstance().SceneMetadataPlans(ctx, sceneIds, state, latestOnly)
}
