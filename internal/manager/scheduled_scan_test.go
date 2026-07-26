package manager

import (
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scheduledScanManager is the subset of Manager methods under test; we test
// the methods directly on a minimal Manager to avoid requiring a live DB.

func newTestManagerForSchedules(t *testing.T) *Manager {
	t.Helper()
	cfg := config.InitializeEmpty()
	// Give the config a real (temp) file path so schedule mutations can
	// persist via Config.Write().
	cfg.SetConfigFile(filepath.Join(t.TempDir(), "config.yml"))
	return &Manager{Config: cfg}
}

func TestScheduledScanCreate(t *testing.T) {
	m := newTestManagerForSchedules(t)

	input := ScanScheduleInput{
		Name:    "Nightly Scan",
		Spec:    "daily@02:00",
		Repeat:  true,
		Enabled: true,
		Paths:   []string{"/media/movies"},
		ScanOptions: config.ScanMetadataOptions{
			Rescan:               false,
			ScanGeneratePreviews: true,
			ScanGenerateCovers:   true,
		},
	}

	sched, err := m.CreateScheduledScan(input)
	require.NoError(t, err)
	require.NotNil(t, sched)

	assert.NotEmpty(t, sched.ID)
	assert.Equal(t, "Nightly Scan", sched.Name)
	assert.Equal(t, "daily@02:00", sched.Spec)
	assert.True(t, sched.Repeat)
	assert.True(t, sched.Enabled)
	assert.Equal(t, []string{"/media/movies"}, sched.Paths)
	assert.True(t, sched.ScanOptions.ScanGeneratePreviews)
}

func TestScheduledScanCreate_InvalidSpec(t *testing.T) {
	m := newTestManagerForSchedules(t)

	input := ScanScheduleInput{
		Name:    "Bad Spec",
		Spec:    "not-a-valid-spec",
		Repeat:  true,
		Enabled: true,
	}

	_, err := m.CreateScheduledScan(input)
	assert.Error(t, err)
}

func TestScheduledScanCreate_EmptyName(t *testing.T) {
	m := newTestManagerForSchedules(t)

	input := ScanScheduleInput{
		Name:    "",
		Spec:    "daily@02:00",
		Repeat:  true,
		Enabled: true,
	}

	_, err := m.CreateScheduledScan(input)
	assert.Error(t, err)
}

func TestScheduledScanUpdate(t *testing.T) {
	m := newTestManagerForSchedules(t)

	original := ScanScheduleInput{
		Name:    "Original",
		Spec:    "daily@02:00",
		Repeat:  true,
		Enabled: true,
	}
	created, err := m.CreateScheduledScan(original)
	require.NoError(t, err)

	updated := ScanScheduleInput{
		Name:    "Updated",
		Spec:    "monday@09:00",
		Repeat:  false,
		Enabled: false,
	}
	result, err := m.UpdateScheduledScan(created.ID, updated)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, created.ID, result.ID)
	assert.Equal(t, "Updated", result.Name)
	assert.Equal(t, "monday@09:00", result.Spec)
	assert.False(t, result.Repeat)
	assert.False(t, result.Enabled)
}

func TestScheduledScanUpdate_NotFound(t *testing.T) {
	m := newTestManagerForSchedules(t)

	input := ScanScheduleInput{
		Name:    "X",
		Spec:    "daily@02:00",
		Repeat:  true,
		Enabled: true,
	}
	_, err := m.UpdateScheduledScan("nonexistent-id", input)
	assert.Error(t, err)
}

func TestScheduledScanDestroy(t *testing.T) {
	m := newTestManagerForSchedules(t)

	s1, err := m.CreateScheduledScan(ScanScheduleInput{Name: "S1", Spec: "daily@02:00", Repeat: true, Enabled: true})
	require.NoError(t, err)
	s2, err := m.CreateScheduledScan(ScanScheduleInput{Name: "S2", Spec: "daily@03:00", Repeat: true, Enabled: true})
	require.NoError(t, err)

	err = m.DestroyScheduledScan(s1.ID)
	require.NoError(t, err)

	schedules, err := m.GetScheduledScans()
	require.NoError(t, err)
	require.Len(t, schedules, 1)
	assert.Equal(t, s2.ID, schedules[0].ID)
}

func TestScheduledScanDestroy_NotFound(t *testing.T) {
	m := newTestManagerForSchedules(t)
	err := m.DestroyScheduledScan("nope")
	assert.Error(t, err)
}

// TestScheduledScan_TimerCancelledOnUpdateAndDestroy verifies that the manager
// tracks the scheduler entry ID so that updating or destroying a schedule
// actually cancels its armed timer instead of leaking a stale one.
func TestScheduledScan_TimerCancelledOnUpdateAndDestroy(t *testing.T) {
	m := newTestManagerForSchedules(t)
	m.StartScanScheduler()
	defer m.StopScanScheduler()

	entryCount := func() int {
		m.scanScheduleMu.Lock()
		defer m.scanScheduleMu.Unlock()
		return len(m.scanScheduleEntries)
	}

	// A far-future daily time so the timer never fires during the test.
	s, err := m.CreateScheduledScan(ScanScheduleInput{
		Name: "Nightly", Spec: "daily@23:59", Repeat: true, Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, entryCount(), "one timer armed after create")

	// Update must cancel the old timer and arm exactly one new one — not two.
	_, err = m.UpdateScheduledScan(s.ID, ScanScheduleInput{
		Name: "Nightly", Spec: "daily@23:58", Repeat: true, Enabled: true,
	})
	require.NoError(t, err)
	assert.Equal(t, 1, entryCount(), "update must not leak a second timer")

	// Disabling must remove the timer entirely.
	_, err = m.UpdateScheduledScan(s.ID, ScanScheduleInput{
		Name: "Nightly", Spec: "daily@23:58", Repeat: true, Enabled: false,
	})
	require.NoError(t, err)
	assert.Equal(t, 0, entryCount(), "disabled schedule must have no armed timer")

	// Re-enable, then destroy.
	_, err = m.UpdateScheduledScan(s.ID, ScanScheduleInput{
		Name: "Nightly", Spec: "daily@23:58", Repeat: true, Enabled: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, entryCount())

	require.NoError(t, m.DestroyScheduledScan(s.ID))
	assert.Equal(t, 0, entryCount(), "destroy must remove the armed timer")
}

func TestGetScheduledScans(t *testing.T) {
	m := newTestManagerForSchedules(t)

	schedules, err := m.GetScheduledScans()
	require.NoError(t, err)
	assert.Empty(t, schedules)

	_, err = m.CreateScheduledScan(ScanScheduleInput{Name: "A", Spec: "daily@01:00", Repeat: true, Enabled: true})
	require.NoError(t, err)
	_, err = m.CreateScheduledScan(ScanScheduleInput{Name: "B", Spec: "friday@09:00", Repeat: true, Enabled: false})
	require.NoError(t, err)

	schedules, err = m.GetScheduledScans()
	require.NoError(t, err)
	assert.Len(t, schedules, 2)
}
