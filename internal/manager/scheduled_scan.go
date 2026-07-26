package manager

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/scheduler"
)

// ScanScheduleInput holds the parameters for creating or updating a scheduled scan.
type ScanScheduleInput struct {
	Name        string
	Spec        string
	Repeat      bool
	Enabled     bool
	Paths       []string
	ScanOptions config.ScanMetadataOptions
}

// CreateScheduledScan validates the input, generates a UUID, persists the schedule,
// and registers it with the scheduler if enabled.
func (s *Manager) CreateScheduledScan(input ScanScheduleInput) (*config.ScanSchedule, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, err := scheduler.ParseScheduleSpec(input.Spec); err != nil {
		return nil, fmt.Errorf("invalid schedule spec: %w", err)
	}

	sched := config.ScanSchedule{
		ID:          uuid.New().String(),
		Name:        input.Name,
		Enabled:     input.Enabled,
		Spec:        input.Spec,
		Repeat:      input.Repeat,
		Paths:       input.Paths,
		ScanOptions: input.ScanOptions,
	}

	if err := s.Config.AddScanSchedule(sched); err != nil {
		return nil, fmt.Errorf("failed to save scheduled scan: %w", err)
	}
	if err := s.Config.Write(); err != nil {
		return nil, fmt.Errorf("failed to persist scheduled scan: %w", err)
	}

	if s.ScanScheduler != nil && input.Enabled {
		s.registerSchedule(sched)
	}

	return &sched, nil
}

// UpdateScheduledScan validates the input, updates the persisted schedule,
// and re-registers it with the scheduler.
func (s *Manager) UpdateScheduledScan(id string, input ScanScheduleInput) (*config.ScanSchedule, error) {
	if input.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if _, err := scheduler.ParseScheduleSpec(input.Spec); err != nil {
		return nil, fmt.Errorf("invalid schedule spec: %w", err)
	}

	existing, err := s.Config.GetScanScheduleByID(id)
	if err != nil {
		return nil, fmt.Errorf("scheduled scan not found: %w", err)
	}

	existing.Name = input.Name
	existing.Spec = input.Spec
	existing.Repeat = input.Repeat
	existing.Enabled = input.Enabled
	existing.Paths = input.Paths
	existing.ScanOptions = input.ScanOptions

	if err := s.Config.UpdateScanSchedule(existing); err != nil {
		return nil, fmt.Errorf("failed to update scheduled scan: %w", err)
	}
	if err := s.Config.Write(); err != nil {
		return nil, fmt.Errorf("failed to persist scheduled scan: %w", err)
	}

	if s.ScanScheduler != nil {
		// Cancel the currently armed timer before (re-)arming, otherwise the
		// stale timer fires the previous scan config.
		s.unregisterSchedule(id)
		if input.Enabled {
			s.registerSchedule(existing)
		}
	}

	return &existing, nil
}

// DestroyScheduledScan removes the schedule from config and the scheduler.
func (s *Manager) DestroyScheduledScan(id string) error {
	if err := s.Config.RemoveScanSchedule(id); err != nil {
		return fmt.Errorf("failed to remove scheduled scan: %w", err)
	}
	if err := s.Config.Write(); err != nil {
		return fmt.Errorf("failed to persist scheduled scan removal: %w", err)
	}

	s.unregisterSchedule(id)

	return nil
}

// GetScheduledScans returns all stored scan schedules.
func (s *Manager) GetScheduledScans() ([]*config.ScanSchedule, error) {
	all := s.Config.GetScanSchedules()
	result := make([]*config.ScanSchedule, len(all))
	for i := range all {
		sched := all[i]
		result[i] = &sched
	}
	return result, nil
}

// StartScanScheduler initialises the scheduler and registers all enabled schedules.
// Must be called after Manager initialisation is complete.
func (s *Manager) StartScanScheduler() {
	s.ScanScheduler = scheduler.New()
	s.ScanScheduler.Start()

	s.scanScheduleMu.Lock()
	s.scanScheduleEntries = make(map[string]string)
	s.scanScheduleMu.Unlock()

	for _, sched := range s.Config.GetScanSchedules() {
		if sched.Enabled {
			s.registerSchedule(sched)
		}
	}
}

// StopScanScheduler halts all scheduled scan goroutines.
func (s *Manager) StopScanScheduler() {
	if s.ScanScheduler != nil {
		s.ScanScheduler.Stop()
	}
}

// registerSchedule computes the next fire time and schedules the scan, recording
// the scheduler entry ID so the timer can later be cancelled by schedule ID.
func (s *Manager) registerSchedule(sched config.ScanSchedule) {
	spec, err := scheduler.ParseScheduleSpec(sched.Spec)
	if err != nil {
		logger.Errorf("invalid schedule spec for %q (%s): %v", sched.Name, sched.ID, err)
		return
	}

	// Schedules are interpreted in the server's local timezone (honouring the
	// TZ environment variable), matching what the UI displays for next-run.
	now := time.Now()
	nextRun := scheduler.NextRunTime(spec.DayOfWeek, spec.Hour, spec.Minute, now)
	repeat := sched.Repeat

	id := sched.ID // capture for closure
	name := sched.Name
	paths := sched.Paths
	opts := sched.ScanOptions

	fireFn := func() {
		logger.Infof("Running scheduled scan %q", name)
		input := ScanMetadataInput{
			Paths:               paths,
			ScanMetadataOptions: opts,
		}
		if _, err := s.Scan(context.Background(), input); err != nil {
			logger.Errorf("scheduled scan %q failed: %v", name, err)
		}
		if err := s.Config.UpdateScanScheduleLastRun(id, time.Now()); err != nil {
			logger.Errorf("failed to record last run for scheduled scan %q: %v", name, err)
		} else if err := s.Config.Write(); err != nil {
			logger.Errorf("failed to persist last run for scheduled scan %q: %v", name, err)
		}

		if repeat {
			// Re-register for next occurrence.
			next, err := s.Config.GetScanScheduleByID(id)
			if err == nil && next.Enabled {
				s.registerSchedule(next)
			}
		}
	}

	entryID, err := s.ScanScheduler.AddAt(nextRun, fireFn)
	if err != nil {
		logger.Errorf("failed to register scheduled scan %q: %v", name, err)
		return
	}

	s.scanScheduleMu.Lock()
	if s.scanScheduleEntries == nil {
		s.scanScheduleEntries = make(map[string]string)
	}
	s.scanScheduleEntries[id] = entryID
	s.scanScheduleMu.Unlock()
}

// unregisterSchedule cancels the armed timer (if any) for the given schedule ID.
func (s *Manager) unregisterSchedule(id string) {
	if s.ScanScheduler == nil {
		return
	}

	s.scanScheduleMu.Lock()
	entryID, ok := s.scanScheduleEntries[id]
	if ok {
		delete(s.scanScheduleEntries, id)
	}
	s.scanScheduleMu.Unlock()

	if ok {
		s.ScanScheduler.Remove(entryID)
	}
}
