package api

import (
	"context"

	"github.com/stashapp/stash/internal/manager"
)

func (r *queryResolver) ScanSchedules(ctx context.Context) ([]*ScanSchedule, error) {
	schedules, err := manager.GetInstance().GetScheduledScans()
	if err != nil {
		return nil, err
	}

	result := make([]*ScanSchedule, len(schedules))
	for i, s := range schedules {
		result[i] = scheduledScanToGQL(s)
	}
	return result, nil
}
