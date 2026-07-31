package proc

import (
	"testing"
	"time"
)

func TestStateClassification(t *testing.T) {
	for _, state := range []State{StateFailed, StateUnavailable, StateDisabled} {
		if !state.Terminal() {
			t.Errorf("%q should be terminal", state)
		}
	}
	for _, state := range []State{StateReady, StateBackoff, StateCrashed, StateStarting} {
		if state.Terminal() {
			t.Errorf("%q should not be terminal", state)
		}
	}
	for _, state := range []State{StateStarting, StateHandshaking, StateReady, StateDraining, StateStopping} {
		if !state.Running() {
			t.Errorf("%q should imply a running process", state)
		}
	}
}

func TestDefaultBackoffPolicy(t *testing.T) {
	policy := DefaultBackoffPolicy()
	fixed := func() float64 { return 0.5 }
	for attempt, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second} {
		if got := policy.Delay(attempt, fixed); got != want {
			t.Errorf("Delay(%d) = %v, want %v", attempt, got, want)
		}
	}
	if got := policy.Delay(1000, fixed); got != time.Minute {
		t.Errorf("capped Delay = %v, want one minute", got)
	}
	for _, random := range []float64{0, 0.5, 1} {
		got := policy.Delay(3, func() float64 { return random })
		if got < time.Second || got < 6400*time.Millisecond || got > 9600*time.Millisecond {
			t.Errorf("jitter %v produced out-of-band delay %v", random, got)
		}
	}
}

func TestCrashWindowExpiresDeterministically(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	window := CrashWindow{Limit: 10, Window: 10 * time.Minute}
	for i := range window.Limit {
		window.Record(now.Add(time.Duration(i) * time.Second))
	}
	if got := window.Count(now.Add(9 * time.Minute)); got != 10 {
		t.Fatalf("Count inside window = %d, want 10", got)
	}
	if got := window.Count(now.Add(11 * time.Minute)); got != 0 {
		t.Fatalf("Count after expiry = %d, want 0", got)
	}
	window.Record(now)
	window.Reset()
	if got := window.Count(now); got != 0 {
		t.Errorf("Count after Reset = %d, want 0", got)
	}
}
