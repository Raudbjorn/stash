package proc

import "time"

// BackoffPolicy controls exponential restart delay and jitter.
type BackoffPolicy struct {
	Min            time.Duration
	Max            time.Duration
	JitterFraction float64
}

// DefaultBackoffPolicy returns the shared one-second to one-minute policy.
func DefaultBackoffPolicy() BackoffPolicy {
	return BackoffPolicy{Min: time.Second, Max: time.Minute, JitterFraction: 0.2}
}

// Delay returns the delay for a zero-based restart attempt.
func (p BackoffPolicy) Delay(attempt int, rnd func() float64) time.Duration {
	if p.Min <= 0 {
		p.Min = time.Second
	}
	if p.Max < p.Min {
		p.Max = p.Min
	}
	if p.JitterFraction < 0 {
		p.JitterFraction = 0
	}

	delay := p.Min
	for range attempt {
		if delay >= p.Max {
			break
		}
		if delay > p.Max/2 {
			delay = p.Max
			break
		}
		delay *= 2
	}
	if delay > p.Max {
		delay = p.Max
	}
	if rnd != nil && p.JitterFraction != 0 {
		factor := 1 + (rnd()*2-1)*p.JitterFraction
		delay = time.Duration(float64(delay) * factor)
	}
	if delay < p.Min {
		return p.Min
	}
	return delay
}

// CrashWindow counts crashes within a rolling time window.
type CrashWindow struct {
	Limit  int
	Window time.Duration
	at     []time.Time
}

// Record adds a crash occurrence.
func (w *CrashWindow) Record(at time.Time) {
	w.at = append(w.at, at)
}

// Count returns crashes newer than the rolling cutoff and prunes older entries.
func (w *CrashWindow) Count(now time.Time) int {
	if w.Window <= 0 {
		return len(w.at)
	}
	cutoff := now.Add(-w.Window)
	kept := w.at[:0]
	for _, at := range w.at {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	w.at = kept
	return len(w.at)
}

// Reset clears all recorded crashes.
func (w *CrashWindow) Reset() {
	w.at = nil
}
