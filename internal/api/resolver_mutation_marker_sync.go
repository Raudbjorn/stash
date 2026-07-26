package api

import (
	"context"
	"strconv"

	"github.com/stashapp/stash/internal/manager"
)

// MarkerSync launches a native marker sync job and returns the job id.
func (r *mutationResolver) MarkerSync(ctx context.Context, input manager.MarkerSyncInput) (string, error) {
	jobID := manager.GetInstance().MarkerSync(ctx, input)
	return strconv.Itoa(jobID), nil
}

// MarkerSyncSubmit launches a native marker sync submit job and returns the job id.
func (r *mutationResolver) MarkerSyncSubmit(ctx context.Context, input manager.MarkerSyncSubmitInput) (string, error) {
	jobID := manager.GetInstance().MarkerSyncSubmit(ctx, input)
	return strconv.Itoa(jobID), nil
}

// MarkerSyncFunscriptIndex launches the funscript index+match job and returns the job id.
func (r *mutationResolver) MarkerSyncFunscriptIndex(ctx context.Context) (string, error) {
	jobID := manager.GetInstance().MarkerSyncFunscriptIndex(ctx)
	return strconv.Itoa(jobID), nil
}
