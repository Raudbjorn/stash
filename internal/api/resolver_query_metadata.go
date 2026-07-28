package api

import (
	"context"

	"github.com/stashapp/stash/internal/manager"
)

func (r *queryResolver) SystemStatus(ctx context.Context) (*manager.SystemStatus, error) {
	return manager.GetInstance().GetSystemStatus(), nil
}
func (r *queryResolver) SceneMetadataEntityModelStatus(ctx context.Context) (*manager.SceneMetadataEntityModelStatus, error) {
	status := manager.GetInstance().SceneMetadataEntityModelStatus()
	return &status, nil
}
