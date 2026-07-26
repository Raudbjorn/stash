package api

import (
	"context"
	"fmt"
	"time"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/scheduler"
)

func (r *mutationResolver) ScanScheduleCreate(ctx context.Context, input ScanScheduleCreateInput) (*ScanSchedule, error) {
	opts := config.ScanMetadataOptions{}
	if input.ScanOptions != nil {
		opts = *input.ScanOptions
	}

	s, err := manager.GetInstance().CreateScheduledScan(manager.ScanScheduleInput{
		Name:        input.Name,
		Spec:        input.Spec,
		Repeat:      input.Repeat,
		Enabled:     input.Enabled,
		Paths:       input.Paths,
		ScanOptions: opts,
	})
	if err != nil {
		return nil, fmt.Errorf("creating scheduled scan: %w", err)
	}

	return scheduledScanToGQL(s), nil
}

func (r *mutationResolver) ScanScheduleUpdate(ctx context.Context, input ScanScheduleUpdateInput) (*ScanSchedule, error) {
	opts := config.ScanMetadataOptions{}
	if input.ScanOptions != nil {
		opts = *input.ScanOptions
	}

	s, err := manager.GetInstance().UpdateScheduledScan(input.ID, manager.ScanScheduleInput{
		Name:        input.Name,
		Spec:        input.Spec,
		Repeat:      input.Repeat,
		Enabled:     input.Enabled,
		Paths:       input.Paths,
		ScanOptions: opts,
	})
	if err != nil {
		return nil, fmt.Errorf("updating scheduled scan: %w", err)
	}

	return scheduledScanToGQL(s), nil
}

func (r *mutationResolver) ScanScheduleDestroy(ctx context.Context, id string) (bool, error) {
	if err := manager.GetInstance().DestroyScheduledScan(id); err != nil {
		return false, fmt.Errorf("destroying scheduled scan: %w", err)
	}

	return true, nil
}

func scheduledScanToGQL(s *config.ScanSchedule) *ScanSchedule {
	gql := &ScanSchedule{
		ID:      s.ID,
		Name:    s.Name,
		Spec:    s.Spec,
		Repeat:  s.Repeat,
		Enabled: s.Enabled,
		Paths:   s.Paths,
		ScanOptions: &config.ScanMetadataOptions{
			Rescan:                    s.ScanOptions.Rescan,
			ScanGenerateCovers:        s.ScanOptions.ScanGenerateCovers,
			ScanGeneratePreviews:      s.ScanOptions.ScanGeneratePreviews,
			ScanGenerateImagePreviews: s.ScanOptions.ScanGenerateImagePreviews,
			ScanGenerateSprites:       s.ScanOptions.ScanGenerateSprites,
			ScanGeneratePhashes:       s.ScanOptions.ScanGeneratePhashes,
			ScanGenerateImagePhashes:  s.ScanOptions.ScanGenerateImagePhashes,
			ScanGenerateThumbnails:    s.ScanOptions.ScanGenerateThumbnails,
			ScanGenerateClipPreviews:  s.ScanOptions.ScanGenerateClipPreviews,
		},
		LastRunAt: s.LastRunAt,
	}

	// Compute next run time for enabled, repeating schedules.
	if s.Enabled {
		if spec, err := scheduler.ParseScheduleSpec(s.Spec); err == nil {
			next := scheduler.NextRunTime(spec.DayOfWeek, spec.Hour, spec.Minute, time.Now().UTC())
			gql.NextRunAt = &next
		}
	}

	return gql
}
