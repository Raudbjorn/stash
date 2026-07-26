package config

import (
	"fmt"
	"time"
)

const ScanSchedules = "scan_schedules"

// ScanSchedule stores a scheduled scan definition in config.
type ScanSchedule struct {
	ID          string              `json:"id" yaml:"id" koanf:"id"`
	Name        string              `json:"name" yaml:"name" koanf:"name"`
	Enabled     bool                `json:"enabled" yaml:"enabled" koanf:"enabled"`
	Spec        string              `json:"spec" yaml:"spec" koanf:"spec"`
	Repeat      bool                `json:"repeat" yaml:"repeat" koanf:"repeat"`
	Paths       []string            `json:"paths,omitempty" yaml:"paths,omitempty" koanf:"paths"`
	ScanOptions ScanMetadataOptions `json:"scanOptions" yaml:"scanOptions" koanf:"scanOptions"`
	LastRunAt   *time.Time          `json:"lastRunAt,omitempty" yaml:"lastRunAt,omitempty" koanf:"lastRunAt"`
}

// GetScanSchedules returns all stored scan schedules.
func (i *Config) GetScanSchedules() []ScanSchedule {
	i.RLock()
	defer i.RUnlock()

	var schedules []ScanSchedule
	_ = i.forKey(ScanSchedules).Unmarshal(ScanSchedules, &schedules)
	if schedules == nil {
		return []ScanSchedule{}
	}
	return schedules
}

// GetScanScheduleByID returns the schedule with the given ID, or an error if not found.
func (i *Config) GetScanScheduleByID(id string) (ScanSchedule, error) {
	for _, s := range i.GetScanSchedules() {
		if s.ID == id {
			return s, nil
		}
	}
	return ScanSchedule{}, fmt.Errorf("scan schedule %q not found", id)
}

// AddScanSchedule appends a new schedule. Returns an error if the ID already exists.
func (i *Config) AddScanSchedule(s ScanSchedule) error {
	i.Lock()
	defer i.Unlock()

	var schedules []ScanSchedule
	_ = i.forKey(ScanSchedules).Unmarshal(ScanSchedules, &schedules)

	for _, existing := range schedules {
		if existing.ID == s.ID {
			return fmt.Errorf("scan schedule with id %q already exists", s.ID)
		}
	}

	schedules = append(schedules, s)
	i.set(ScanSchedules, schedules)
	return nil
}

// UpdateScanSchedule replaces the schedule that has the same ID. Returns an error if not found.
func (i *Config) UpdateScanSchedule(s ScanSchedule) error {
	i.Lock()
	defer i.Unlock()

	var schedules []ScanSchedule
	_ = i.forKey(ScanSchedules).Unmarshal(ScanSchedules, &schedules)

	found := false
	for idx, existing := range schedules {
		if existing.ID == s.ID {
			schedules[idx] = s
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("scan schedule %q not found", s.ID)
	}

	i.set(ScanSchedules, schedules)
	return nil
}

// RemoveScanSchedule deletes the schedule with the given ID. Returns an error if not found.
func (i *Config) RemoveScanSchedule(id string) error {
	i.Lock()
	defer i.Unlock()

	var schedules []ScanSchedule
	_ = i.forKey(ScanSchedules).Unmarshal(ScanSchedules, &schedules)

	newSchedules := make([]ScanSchedule, 0, len(schedules))
	found := false
	for _, s := range schedules {
		if s.ID == id {
			found = true
			continue
		}
		newSchedules = append(newSchedules, s)
	}
	if !found {
		return fmt.Errorf("scan schedule %q not found", id)
	}

	i.set(ScanSchedules, newSchedules)
	return nil
}

// UpdateScanScheduleLastRun updates the LastRunAt timestamp for a schedule.
func (i *Config) UpdateScanScheduleLastRun(id string, t time.Time) error {
	s, err := i.GetScanScheduleByID(id)
	if err != nil {
		return err
	}
	s.LastRunAt = &t
	return i.UpdateScanSchedule(s)
}

// SetScanScheduleEnabled enables or disables a schedule by ID.
func (i *Config) SetScanScheduleEnabled(id string, enabled bool) error {
	s, err := i.GetScanScheduleByID(id)
	if err != nil {
		return err
	}
	s.Enabled = enabled
	return i.UpdateScanSchedule(s)
}
