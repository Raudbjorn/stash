package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeTestSchedule(id, name, spec string) ScanSchedule {
	return ScanSchedule{
		ID:      id,
		Name:    name,
		Enabled: true,
		Spec:    spec,
		Repeat:  true,
		ScanOptions: ScanMetadataOptions{
			ScanGeneratePreviews: true,
		},
	}
}

func TestScanSchedules_EmptyInitially(t *testing.T) {
	c := InitializeEmpty()
	schedules := c.GetScanSchedules()
	assert.Empty(t, schedules)
}

func TestScanSchedules_AddAndGet(t *testing.T) {
	c := InitializeEmpty()

	s := makeTestSchedule("id-1", "Nightly", "daily@02:00")
	err := c.AddScanSchedule(s)
	require.NoError(t, err)

	got := c.GetScanSchedules()
	require.Len(t, got, 1)
	assert.Equal(t, "id-1", got[0].ID)
	assert.Equal(t, "Nightly", got[0].Name)
	assert.Equal(t, "daily@02:00", got[0].Spec)
	assert.True(t, got[0].Repeat)
	assert.True(t, got[0].ScanOptions.ScanGeneratePreviews)
}

func TestScanSchedules_AddDuplicateIDErrors(t *testing.T) {
	c := InitializeEmpty()

	s := makeTestSchedule("id-1", "First", "daily@02:00")
	require.NoError(t, c.AddScanSchedule(s))

	dup := makeTestSchedule("id-1", "Duplicate", "daily@03:00")
	err := c.AddScanSchedule(dup)
	assert.Error(t, err)
}

func TestScanSchedules_Update(t *testing.T) {
	c := InitializeEmpty()

	s := makeTestSchedule("id-1", "Old", "daily@02:00")
	require.NoError(t, c.AddScanSchedule(s))

	updated := makeTestSchedule("id-1", "New", "monday@09:00")
	updated.Repeat = false
	err := c.UpdateScanSchedule(updated)
	require.NoError(t, err)

	got := c.GetScanSchedules()
	require.Len(t, got, 1)
	assert.Equal(t, "New", got[0].Name)
	assert.Equal(t, "monday@09:00", got[0].Spec)
	assert.False(t, got[0].Repeat)
}

func TestScanSchedules_UpdateNonExistentErrors(t *testing.T) {
	c := InitializeEmpty()

	s := makeTestSchedule("missing", "X", "daily@01:00")
	err := c.UpdateScanSchedule(s)
	assert.Error(t, err)
}

func TestScanSchedules_Remove(t *testing.T) {
	c := InitializeEmpty()

	require.NoError(t, c.AddScanSchedule(makeTestSchedule("id-1", "A", "daily@02:00")))
	require.NoError(t, c.AddScanSchedule(makeTestSchedule("id-2", "B", "daily@03:00")))

	err := c.RemoveScanSchedule("id-1")
	require.NoError(t, err)

	got := c.GetScanSchedules()
	require.Len(t, got, 1)
	assert.Equal(t, "id-2", got[0].ID)
}

func TestScanSchedules_RemoveNonExistentErrors(t *testing.T) {
	c := InitializeEmpty()
	err := c.RemoveScanSchedule("nope")
	assert.Error(t, err)
}

func TestScanSchedules_GetByID(t *testing.T) {
	c := InitializeEmpty()

	require.NoError(t, c.AddScanSchedule(makeTestSchedule("id-1", "A", "daily@02:00")))
	require.NoError(t, c.AddScanSchedule(makeTestSchedule("id-2", "B", "daily@03:00")))

	got, err := c.GetScanScheduleByID("id-2")
	require.NoError(t, err)
	assert.Equal(t, "B", got.Name)
}

func TestScanSchedules_GetByIDNotFound(t *testing.T) {
	c := InitializeEmpty()
	_, err := c.GetScanScheduleByID("nope")
	assert.Error(t, err)
}

func TestScanSchedules_UpdateLastRun(t *testing.T) {
	c := InitializeEmpty()
	require.NoError(t, c.AddScanSchedule(makeTestSchedule("id-1", "A", "daily@02:00")))

	now := time.Now().Truncate(time.Second)
	err := c.UpdateScanScheduleLastRun("id-1", now)
	require.NoError(t, err)

	got, err := c.GetScanScheduleByID("id-1")
	require.NoError(t, err)
	require.NotNil(t, got.LastRunAt)
	assert.Equal(t, now, *got.LastRunAt)
}

func TestScanSchedules_SetEnabled(t *testing.T) {
	c := InitializeEmpty()
	require.NoError(t, c.AddScanSchedule(makeTestSchedule("id-1", "A", "daily@02:00")))

	err := c.SetScanScheduleEnabled("id-1", false)
	require.NoError(t, err)

	got, err := c.GetScanScheduleByID("id-1")
	require.NoError(t, err)
	assert.False(t, got.Enabled)
}
