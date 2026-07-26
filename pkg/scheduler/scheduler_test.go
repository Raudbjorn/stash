package scheduler_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/scheduler"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- NextRunTime ---

func TestNextRunTime_DailyFutureSameDay(t *testing.T) {
	// now = 08:00 Monday; schedule = daily 09:30 → next run today at 09:30
	now := time.Date(2025, 1, 6, 8, 0, 0, 0, time.UTC) // Monday
	next := scheduler.NextRunTime(scheduler.AnyDay, 9, 30, now)
	want := time.Date(2025, 1, 6, 9, 30, 0, 0, time.UTC)
	assert.Equal(t, want, next)
}

func TestNextRunTime_DailyPastToday(t *testing.T) {
	// now = 10:00; schedule = daily 09:30 → next run tomorrow at 09:30
	now := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC)
	next := scheduler.NextRunTime(scheduler.AnyDay, 9, 30, now)
	want := time.Date(2025, 1, 7, 9, 30, 0, 0, time.UTC)
	assert.Equal(t, want, next)
}

func TestNextRunTime_WeeklyCurrentDayFuture(t *testing.T) {
	// now = Monday 08:00; schedule = Monday 09:30 → next run today
	now := time.Date(2025, 1, 6, 8, 0, 0, 0, time.UTC) // Monday = weekday 1
	next := scheduler.NextRunTime(time.Monday, 9, 30, now)
	want := time.Date(2025, 1, 6, 9, 30, 0, 0, time.UTC)
	assert.Equal(t, want, next)
}

func TestNextRunTime_WeeklyCurrentDayPast(t *testing.T) {
	// now = Monday 10:00; schedule = Monday 09:30 → next run next Monday
	now := time.Date(2025, 1, 6, 10, 0, 0, 0, time.UTC) // Monday
	next := scheduler.NextRunTime(time.Monday, 9, 30, now)
	want := time.Date(2025, 1, 13, 9, 30, 0, 0, time.UTC)
	assert.Equal(t, want, next)
}

func TestNextRunTime_WeeklyOtherDay(t *testing.T) {
	// now = Monday; schedule = Friday 02:00 → next run this Friday
	now := time.Date(2025, 1, 6, 8, 0, 0, 0, time.UTC) // Monday
	next := scheduler.NextRunTime(time.Friday, 2, 0, now)
	want := time.Date(2025, 1, 10, 2, 0, 0, 0, time.UTC) // Friday same week
	assert.Equal(t, want, next)
}

func TestNextRunTime_WeeklyWrapAroundWeek(t *testing.T) {
	// now = Friday; schedule = Monday 09:00 → next run following Monday
	now := time.Date(2025, 1, 10, 8, 0, 0, 0, time.UTC) // Friday
	next := scheduler.NextRunTime(time.Monday, 9, 0, now)
	want := time.Date(2025, 1, 13, 9, 0, 0, 0, time.UTC) // Monday
	assert.Equal(t, want, next)
}

// --- ParseScheduleSpec ---

func TestParseScheduleSpec_Valid(t *testing.T) {
	tests := []struct {
		spec    string
		wantDay time.Weekday
		wantH   int
		wantM   int
	}{
		{"daily@02:30", scheduler.AnyDay, 2, 30},
		{"monday@09:00", time.Monday, 9, 0},
		{"friday@23:59", time.Friday, 23, 59},
		{"sunday@00:00", time.Sunday, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.spec, func(t *testing.T) {
			s, err := scheduler.ParseScheduleSpec(tc.spec)
			require.NoError(t, err)
			assert.Equal(t, tc.wantDay, s.DayOfWeek)
			assert.Equal(t, tc.wantH, s.Hour)
			assert.Equal(t, tc.wantM, s.Minute)
		})
	}
}

func TestParseScheduleSpec_Invalid(t *testing.T) {
	cases := []string{
		"",
		"badday@09:00",
		"daily@25:00",
		"daily@09:60",
		"daily",
		"@09:00",
		"daily@9",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			_, err := scheduler.ParseScheduleSpec(spec)
			assert.Error(t, err)
		})
	}
}

// --- FormatScheduleSpec ---

func TestFormatScheduleSpec_RoundTrip(t *testing.T) {
	specs := []string{
		"daily@02:30",
		"monday@09:00",
		"friday@23:59",
		"sunday@00:00",
	}
	for _, spec := range specs {
		t.Run(spec, func(t *testing.T) {
			s, err := scheduler.ParseScheduleSpec(spec)
			require.NoError(t, err)
			assert.Equal(t, spec, scheduler.FormatScheduleSpec(s))
		})
	}
}

// --- Scheduler fire + repeat ---

func TestScheduler_FiresOnce(t *testing.T) {
	s := scheduler.New()
	s.Start()
	defer s.Stop()

	var fired atomic.Int32
	now := time.Now().UTC()
	// Schedule to fire 30ms from now (daily, any specific time doesn't matter; use absolute trigger via test helper)
	triggerAt := now.Add(30 * time.Millisecond)

	id, err := s.AddAt(triggerAt, func() {
		fired.Add(1)
	})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), fired.Load(), "should fire exactly once")

	// Wait more — should NOT fire again
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int32(1), fired.Load(), "should not fire again when repeat=false")
}

func TestScheduler_FiresRepeatedly(t *testing.T) {
	s := scheduler.New()
	s.Start()
	defer s.Stop()

	var fired atomic.Int32
	interval := 50 * time.Millisecond

	id, err := s.AddInterval(interval, func() {
		fired.Add(1)
	})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	time.Sleep(175 * time.Millisecond)
	count := fired.Load()
	// Should have fired 3 times (at ~50ms, ~100ms, ~150ms)
	assert.GreaterOrEqual(t, count, int32(2), "should fire at least 2 times")
	assert.LessOrEqual(t, count, int32(4), "should not fire too many times")
}

func TestScheduler_RemoveEntry(t *testing.T) {
	s := scheduler.New()
	s.Start()
	defer s.Stop()

	var fired atomic.Int32
	id, err := s.AddInterval(30*time.Millisecond, func() {
		fired.Add(1)
	})
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	s.Remove(id)
	countAfterRemove := fired.Load()

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, countAfterRemove, fired.Load(), "should not fire after removal")
}

func TestScheduler_RemoveByReturnedIDCancelsBeforeFire(t *testing.T) {
	s := scheduler.New()
	s.Start()
	defer s.Stop()

	var fired atomic.Int32
	id, err := s.AddAt(time.Now().Add(50*time.Millisecond), func() {
		fired.Add(1)
	})
	require.NoError(t, err)

	// Cancelling with the ID returned by AddAt must prevent the run.
	s.Remove(id)

	time.Sleep(120 * time.Millisecond)
	assert.Equal(t, int32(0), fired.Load(), "removed entry must not fire")
}

func TestScheduler_RemoveWrongIDDoesNotCancel(t *testing.T) {
	s := scheduler.New()
	s.Start()
	defer s.Stop()

	var fired atomic.Int32
	_, err := s.AddAt(time.Now().Add(50*time.Millisecond), func() {
		fired.Add(1)
	})
	require.NoError(t, err)

	// Removing an unrelated ID must not affect the armed entry. This is the
	// contract the manager relies on: it must track the ID returned by AddAt,
	// not the schedule's own ID (the original bug passed the wrong ID here).
	s.Remove("some-unrelated-id")

	time.Sleep(120 * time.Millisecond)
	assert.Equal(t, int32(1), fired.Load(), "entry keyed by a different ID must still fire")
}

func TestNextRunTime_UsesNowLocation(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)

	// Thursday 01:00 local; daily 02:00 → same day 02:00 in the same location.
	now := time.Date(2026, 1, 15, 1, 0, 0, 0, loc)
	next := scheduler.NextRunTime(scheduler.AnyDay, 2, 0, now)

	assert.Equal(t, loc.String(), next.Location().String(), "next run must be in now's location")
	assert.Equal(t, time.Date(2026, 1, 15, 2, 0, 0, 0, loc), next)
}

func TestScheduler_StopAndStart(t *testing.T) {
	s := scheduler.New()
	s.Start()

	var fired atomic.Int32
	_, err := s.AddInterval(30*time.Millisecond, func() {
		fired.Add(1)
	})
	require.NoError(t, err)

	time.Sleep(50 * time.Millisecond)
	s.Stop()
	countAtStop := fired.Load()

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, countAtStop, fired.Load(), "should not fire after stop")
}
