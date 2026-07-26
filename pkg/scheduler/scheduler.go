// Package scheduler provides a lightweight recurring and one-shot task scheduler.
package scheduler

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// AnyDay is the sentinel value for "every day" (no specific weekday).
const AnyDay = time.Weekday(7)

// ScheduleSpec describes when a task should run.
type ScheduleSpec struct {
	DayOfWeek time.Weekday // AnyDay for daily
	Hour      int
	Minute    int
}

var dayNames = map[string]time.Weekday{
	"sunday":    time.Sunday,
	"monday":    time.Monday,
	"tuesday":   time.Tuesday,
	"wednesday": time.Wednesday,
	"thursday":  time.Thursday,
	"friday":    time.Friday,
	"saturday":  time.Saturday,
}

// ParseScheduleSpec parses a schedule spec string in the form "day@HH:MM" or "daily@HH:MM".
func ParseScheduleSpec(spec string) (ScheduleSpec, error) {
	if spec == "" {
		return ScheduleSpec{}, fmt.Errorf("spec is empty")
	}

	parts := strings.SplitN(spec, "@", 2)
	if len(parts) != 2 {
		return ScheduleSpec{}, fmt.Errorf("invalid spec %q: expected format day@HH:MM", spec)
	}

	dayStr := strings.ToLower(parts[0])
	timeStr := parts[1]

	var dayOfWeek time.Weekday
	if dayStr == "daily" {
		dayOfWeek = AnyDay
	} else {
		d, ok := dayNames[dayStr]
		if !ok {
			return ScheduleSpec{}, fmt.Errorf("invalid day %q in spec %q", dayStr, spec)
		}
		dayOfWeek = d
	}

	timeParts := strings.SplitN(timeStr, ":", 2)
	if len(timeParts) != 2 {
		return ScheduleSpec{}, fmt.Errorf("invalid time %q in spec %q: expected HH:MM", timeStr, spec)
	}

	hour, err := strconv.Atoi(timeParts[0])
	if err != nil || hour < 0 || hour > 23 {
		return ScheduleSpec{}, fmt.Errorf("invalid hour %q in spec %q", timeParts[0], spec)
	}
	if len(timeParts[0]) != 2 {
		return ScheduleSpec{}, fmt.Errorf("invalid hour %q in spec %q: must be zero-padded (HH)", timeParts[0], spec)
	}

	minute, err := strconv.Atoi(timeParts[1])
	if err != nil || minute < 0 || minute > 59 {
		return ScheduleSpec{}, fmt.Errorf("invalid minute %q in spec %q", timeParts[1], spec)
	}
	if len(timeParts[1]) != 2 {
		return ScheduleSpec{}, fmt.Errorf("invalid minute %q in spec %q: must be zero-padded (MM)", timeParts[1], spec)
	}

	return ScheduleSpec{
		DayOfWeek: dayOfWeek,
		Hour:      hour,
		Minute:    minute,
	}, nil
}

// FormatScheduleSpec serialises a ScheduleSpec back to its string form.
func FormatScheduleSpec(s ScheduleSpec) string {
	timeStr := fmt.Sprintf("%02d:%02d", s.Hour, s.Minute)
	if s.DayOfWeek == AnyDay {
		return "daily@" + timeStr
	}
	return strings.ToLower(s.DayOfWeek.String()) + "@" + timeStr
}

// NextRunTime returns the next UTC time at which the schedule should fire,
// relative to now. Seconds and sub-seconds are zeroed.
func NextRunTime(dayOfWeek time.Weekday, hour, minute int, now time.Time) time.Time {
	now = now.UTC()

	candidate := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.UTC)

	if dayOfWeek == AnyDay {
		if !candidate.After(now) {
			candidate = candidate.Add(24 * time.Hour)
		}
		return candidate
	}

	// Find the next occurrence of dayOfWeek at the specified time.
	for i := 0; i <= 7; i++ {
		t := candidate.Add(time.Duration(i) * 24 * time.Hour)
		if t.Weekday() == dayOfWeek && t.After(now) {
			return t
		}
	}

	// Fallback: one week from now at the target time (should never be reached).
	return candidate.Add(7 * 24 * time.Hour)
}

// entry is a scheduled task managed by the Scheduler.
type entry struct {
	id       string
	fn       func()
	repeat   bool
	interval time.Duration // used when repeat=true with interval mode
	nextRun  time.Time
	cancel   context.CancelFunc
}

// Scheduler manages a collection of timed or recurring tasks.
type Scheduler struct {
	mu      sync.Mutex
	entries map[string]*entry
	ctx     context.Context
	cancel  context.CancelFunc
	started bool
}

// New creates a new stopped Scheduler.
func New() *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // start cancelled; Start() will replace it
	return &Scheduler{
		entries: make(map[string]*entry),
		ctx:     ctx,
		cancel:  cancel,
	}
}

// Start begins the scheduler's dispatch loop.
func (s *Scheduler) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.started = true
}

// Stop halts all scheduled tasks.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return
	}
	s.cancel()
	s.started = false
}

// AddAt schedules fn to run once at the given time. If repeat is false the
// entry removes itself after firing. Returns the entry ID.
func (s *Scheduler) AddAt(at time.Time, repeat bool, fn func()) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return "", fmt.Errorf("scheduler not started")
	}

	id := uuid.New().String()
	ctx, cancel := context.WithCancel(s.ctx)

	e := &entry{
		id:      id,
		fn:      fn,
		repeat:  repeat,
		nextRun: at,
		cancel:  cancel,
	}
	s.entries[id] = e

	go s.runAt(ctx, e, at)
	return id, nil
}

// AddInterval schedules fn to run repeatedly at the given interval. Returns the entry ID.
func (s *Scheduler) AddInterval(interval time.Duration, fn func()) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.started {
		return "", fmt.Errorf("scheduler not started")
	}

	id := uuid.New().String()
	ctx, cancel := context.WithCancel(s.ctx)

	e := &entry{
		id:       id,
		fn:       fn,
		repeat:   true,
		interval: interval,
		cancel:   cancel,
	}
	s.entries[id] = e

	go s.runInterval(ctx, id, interval, fn)
	return id, nil
}

// Remove cancels and removes the entry with the given ID.
func (s *Scheduler) Remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.entries[id]
	if !ok {
		return
	}
	e.cancel()
	delete(s.entries, id)
}

func (s *Scheduler) runAt(ctx context.Context, e *entry, at time.Time) {
	delay := time.Until(at)
	if delay < 0 {
		delay = 0
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(delay):
	}

	select {
	case <-ctx.Done():
		return
	default:
		e.fn()
		if !e.repeat {
			s.Remove(e.id)
		}
	}
}

func (s *Scheduler) runInterval(ctx context.Context, id string, interval time.Duration, fn func()) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fn()
		}
	}
}
