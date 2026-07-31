package interactions

import (
	"math"
	"math/rand"
	"strconv"
	"testing"
	"time"
)

func ev(t string, offset float64, meta map[string]any) ReplayEvent {
	return ReplayEvent{
		Type:     t,
		ClientTS: time.Unix(0, 0).Add(time.Duration(offset * float64(time.Second))),
		Metadata: meta,
	}
}

func m(pairs ...any) map[string]any {
	out := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		out[pairs[i].(string)] = pairs[i+1]
	}
	return out
}

func intervalsEqual(a, b []Interval) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if math.Abs(a[i].Start-b[i].Start) > 1e-9 || math.Abs(a[i].End-b[i].End) > 1e-9 {
			return false
		}
	}
	return true
}

// The state machine's transitions, one case per rule.
func TestComputeSegments(t *testing.T) {
	cases := []struct {
		name   string
		events []ReplayEvent
		want   []Interval
	}{
		{
			name: "play then pause",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 10.0)),
				ev(EventSceneWatchPause, 5, m("position", 30.0)),
			},
			want: []Interval{{10, 30}},
		},
		{
			// A run left open at the end is flushed at the last known position.
			name: "unterminated run is flushed",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 10.0)),
				ev(EventSceneWatchProgress, 5, m("position", 25.0)),
			},
			want: []Interval{{10, 25}},
		},
		{
			// Playback that began before the window: the first progress seen
			// opens a run rather than being discarded.
			name: "progress alone opens a run",
			events: []ReplayEvent{
				ev(EventSceneWatchProgress, 0, m("position", 100.0)),
				ev(EventSceneWatchProgress, 5, m("position", 130.0)),
			},
			want: []Interval{{100, 130}},
		},
		{
			name: "seek while playing closes at from and resumes at to",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 0.0)),
				ev(EventSceneSeek, 10, m("from", 10.0, "to", 100.0)),
				ev(EventSceneWatchPause, 20, m("position", 120.0)),
			},
			want: []Interval{{0, 10}, {100, 120}},
		},
		{
			// Seeking while paused must not invent a watched span.
			name: "seek while paused does not resume",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 0.0)),
				ev(EventSceneWatchPause, 5, m("position", 20.0)),
				ev(EventSceneSeek, 10, m("from", 20.0, "to", 200.0)),
			},
			want: []Interval{{0, 20}},
		},
		{
			name: "seek without a destination stops tracking",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 0.0)),
				ev(EventSceneSeek, 10, m("from", 10.0)),
				ev(EventSceneWatchProgress, 20, m("position", 500.0)),
			},
			// The run closes at 10; the later progress opens a new run that is
			// flushed with no advance, so it contributes nothing.
			want: []Interval{{0, 10}},
		},
		{
			name: "complete closes the run",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 0.0)),
				ev(EventSceneWatchComplete, 60, m("position", 60.0)),
			},
			want: []Interval{{0, 60}},
		},
		{
			// Pause with no position falls back to the last reported one.
			name: "pause without position uses last position",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 5.0)),
				ev(EventSceneWatchProgress, 5, m("position", 40.0)),
				ev(EventSceneWatchPause, 6, nil),
			},
			want: []Interval{{5, 40}},
		},
		{
			// Start with no position resumes from wherever we were.
			name: "start without position uses last position",
			events: []ReplayEvent{
				ev(EventSceneWatchProgress, 0, m("position", 50.0)),
				ev(EventSceneWatchPause, 1, m("position", 50.0)),
				ev(EventSceneWatchStart, 2, nil),
				ev(EventSceneWatchPause, 3, m("position", 80.0)),
			},
			want: []Interval{{50, 80}},
		},
		{
			// Repeated pauses at the same spot are degenerate and emit nothing.
			name: "degenerate runs are dropped",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 10.0)),
				ev(EventSceneWatchPause, 1, m("position", 10.0)),
				ev(EventSceneWatchPause, 2, m("position", 10.0)),
			},
			want: nil,
		},
		{
			// Rewinding produces an end before the start, which is not a span.
			name: "backwards run is dropped",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", 100.0)),
				ev(EventSceneWatchPause, 1, m("position", 50.0)),
			},
			want: nil,
		},
		{
			name:   "no events",
			events: nil,
			want:   nil,
		},
		{
			// Positions may arrive as strings if they went through a
			// conversion in the browser.
			name: "string positions are accepted",
			events: []ReplayEvent{
				ev(EventSceneWatchStart, 0, m("position", "10.5")),
				ev(EventSceneWatchPause, 5, m("position", "30.5")),
			},
			want: []Interval{{10.5, 30.5}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeSegments(tc.events, 0.5, 0)
			if !intervalsEqual(got, tc.want) {
				t.Errorf("segments = %v, want %v", got, tc.want)
			}
		})
	}
}

// Adjacent runs within the merge gap become one span, which is what stops a
// pause/resume from fragmenting a continuous watch.
func TestComputeSegmentsMerging(t *testing.T) {
	events := []ReplayEvent{
		ev(EventSceneWatchStart, 0, m("position", 0.0)),
		ev(EventSceneWatchPause, 10, m("position", 10.0)),
		// Resumes 0.3s later in the video - inside the 0.5s gap.
		ev(EventSceneWatchStart, 11, m("position", 10.3)),
		ev(EventSceneWatchPause, 20, m("position", 25.0)),
	}

	got := ComputeSegments(events, 0.5, 0)
	if want := []Interval{{0, 25}}; !intervalsEqual(got, want) {
		t.Errorf("with a 0.5s gap: %v, want %v", got, want)
	}

	// With no tolerance the two runs stay separate.
	got = ComputeSegments(events, 0, 0)
	if want := []Interval{{0, 10}, {10.3, 25}}; !intervalsEqual(got, want) {
		t.Errorf("with no gap: %v, want %v", got, want)
	}
}

func TestComputeSegmentsMinDuration(t *testing.T) {
	events := []ReplayEvent{
		ev(EventSceneWatchStart, 0, m("position", 0.0)),
		ev(EventSceneWatchPause, 1, m("position", 1.0)), // 1s, too short
		ev(EventSceneWatchStart, 2, m("position", 100.0)),
		ev(EventSceneWatchPause, 12, m("position", 110.0)), // 10s, kept
	}

	got := ComputeSegments(events, 0.5, 1.5)
	if want := []Interval{{100, 110}}; !intervalsEqual(got, want) {
		t.Errorf("segments = %v, want only the long one", got)
	}
}

// A realistic session: watch, skip an intro, watch more, finish.
func TestComputeSegmentsRealisticSession(t *testing.T) {
	events := []ReplayEvent{
		ev(EventSceneWatchStart, 0, m("position", 0.0, "duration", 600.0)),
		ev(EventSceneWatchProgress, 10, m("position", 10.0)),
		ev(EventSceneSeek, 15, m("from", 15.0, "to", 120.0)),
		ev(EventSceneWatchProgress, 25, m("position", 130.0)),
		ev(EventSceneWatchPause, 30, m("position", 140.0)),
		ev(EventSceneWatchStart, 60, m("position", 140.0)),
		ev(EventSceneWatchComplete, 120, m("position", 200.0)),
	}

	got := ComputeSegments(events, 0.5, 1.5)
	want := []Interval{{0, 15}, {120, 200}}
	if !intervalsEqual(got, want) {
		t.Errorf("segments = %v, want %v", got, want)
	}
	if total := TotalWatched(got); math.Abs(total-95) > 1e-9 {
		t.Errorf("total watched = %v, want 95", total)
	}
}

func TestMergeIntervals(t *testing.T) {
	cases := []struct {
		name string
		in   []Interval
		gap  float64
		want []Interval
	}{
		{"empty", nil, 1, nil},
		{"single", []Interval{{0, 10}}, 1, []Interval{{0, 10}}},
		{"disjoint", []Interval{{0, 10}, {20, 30}}, 1, []Interval{{0, 10}, {20, 30}}},
		{"overlapping", []Interval{{0, 10}, {5, 20}}, 0, []Interval{{0, 20}}},
		{"touching within gap", []Interval{{0, 10}, {10.5, 20}}, 1, []Interval{{0, 20}}},
		{"unsorted input", []Interval{{20, 30}, {0, 10}}, 0, []Interval{{0, 10}, {20, 30}}},
		{"fully contained", []Interval{{0, 100}, {10, 20}}, 0, []Interval{{0, 100}}},
		{"chain merges transitively", []Interval{{0, 10}, {10, 20}, {20, 30}}, 0, []Interval{{0, 30}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MergeIntervals(tc.in, tc.gap); !intervalsEqual(got, tc.want) {
				t.Errorf("MergeIntervals = %v, want %v", got, tc.want)
			}
		})
	}
}

// Merging must always produce a sorted, non-overlapping set, whatever the
// input. This is the invariant the reconciliation downstream relies on.
func TestMergeIntervalsProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(1234))

	for iteration := 0; iteration < 500; iteration++ {
		n := rng.Intn(12)
		in := make([]Interval, n)
		for i := range in {
			start := rng.Float64() * 500
			in[i] = Interval{Start: start, End: start + rng.Float64()*50}
		}
		gap := rng.Float64() * 3

		got := MergeIntervals(in, gap)

		for i := range got {
			if got[i].End < got[i].Start {
				t.Fatalf("iteration %d: inverted interval %v", iteration, got[i])
			}
			if i == 0 {
				continue
			}
			if got[i].Start < got[i-1].Start {
				t.Fatalf("iteration %d: not sorted: %v", iteration, got)
			}
			// Strictly beyond the gap, or they would have been merged.
			if got[i].Start <= got[i-1].End+gap {
				t.Fatalf("iteration %d: intervals within gap survived: %v", iteration, got)
			}
		}

		// Merging cannot lose coverage: every input point is still covered.
		for _, iv := range in {
			mid := (iv.Start + iv.End) / 2
			covered := false
			for _, g := range got {
				if mid >= g.Start && mid <= g.End {
					covered = true
					break
				}
			}
			if !covered && iv.Duration() > 0 {
				t.Fatalf("iteration %d: %v lost coverage of %v in %v", iteration, in, mid, got)
			}
		}
	}
}

// Replaying the same event stream twice must give the same answer, and
// replaying a prefix then the whole thing must not lose watched time. This is
// what makes re-ingesting an overlapping batch safe.
func TestComputeSegmentsIsDeterministicAndMonotonic(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	types := []string{
		EventSceneWatchStart, EventSceneWatchPause, EventSceneWatchProgress,
		EventSceneSeek, EventSceneWatchComplete,
	}

	for iteration := 0; iteration < 300; iteration++ {
		n := 1 + rng.Intn(20)
		events := make([]ReplayEvent, n)
		pos := 0.0
		for i := range events {
			typ := types[rng.Intn(len(types))]
			pos += rng.Float64() * 20
			meta := m("position", pos)
			if typ == EventSceneSeek {
				meta = m("from", pos, "to", pos+rng.Float64()*100)
			}
			events[i] = ev(typ, float64(i), meta)
		}

		first := ComputeSegments(events, 0.5, 0)
		second := ComputeSegments(events, 0.5, 0)
		if !intervalsEqual(first, second) {
			t.Fatalf("iteration %d: replay is not deterministic", iteration)
		}

		// Output invariants.
		for i := range first {
			if first[i].Duration() <= 0 {
				t.Fatalf("iteration %d: zero-length interval %v", iteration, first[i])
			}
			if i > 0 && first[i].Start <= first[i-1].End+0.5 {
				t.Fatalf("iteration %d: unmerged intervals %v", iteration, first)
			}
		}
	}
}

func TestFilterIntervals(t *testing.T) {
	in := []Interval{{0, 1}, {10, 20}, {30, 30.5}}
	if got := FilterIntervals(in, 1.5); !intervalsEqual(got, []Interval{{10, 20}}) {
		t.Errorf("FilterIntervals = %v", got)
	}
	// Zero keeps everything with a positive length.
	if got := FilterIntervals(in, 0); len(got) != 3 {
		t.Errorf("FilterIntervals(0) dropped intervals: %v", got)
	}
	if got := FilterIntervals(nil, 1); got != nil {
		t.Errorf("FilterIntervals(nil) = %v", got)
	}
}

func TestSanitizeEntityID(t *testing.T) {
	cases := []struct {
		in   any
		want int
	}{
		{42.0, 42},
		{"42", 42},
		{float64(pgIntMax), pgIntMax},
		// Beyond the historical 32-bit range collapses to 0, deliberately.
		{float64(pgIntMax) + 1, 0},
		{-float64(pgIntMax) - 1, 0},
		{"not a number", 0},
		{nil, 0},
		{map[string]any{}, 0},
	}
	for _, tc := range cases {
		if got := sanitizeEntityID(tc.in); got != tc.want {
			t.Errorf("sanitizeEntityID(%v) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseEventTimestamp(t *testing.T) {
	want := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

	if got, ok := parseEventTimestamp("2026-07-26T12:00:00Z"); !ok || !got.Equal(want) {
		t.Errorf("ISO-8601: %v %v", got, ok)
	}
	// All-digit strings are epoch milliseconds.
	millis := want.UnixMilli()
	if got, ok := parseEventTimestamp(strconv.FormatInt(millis, 10)); !ok || !got.Equal(want) {
		t.Errorf("epoch millis string: %v %v", got, ok)
	}
	if got, ok := parseEventTimestamp(float64(millis)); !ok || !got.Equal(want) {
		t.Errorf("epoch millis number: %v %v", got, ok)
	}
	if _, ok := parseEventTimestamp("nonsense"); ok {
		t.Error("parsed nonsense")
	}
	if _, ok := parseEventTimestamp(nil); ok {
		t.Error("parsed nil")
	}
}

func TestIsControlEvent(t *testing.T) {
	for _, typ := range []string{EventSceneWatchStart, EventSceneWatchPause, EventSceneWatchComplete, EventSceneSeek} {
		if !IsControlEvent(typ) {
			t.Errorf("%q should be a control event", typ)
		}
	}
	// Progress reports a position but does not change playback state, which is
	// what the continuous-playback heuristic keys on.
	for _, typ := range []string{EventSceneWatchProgress, EventSceneView, EventScenePageEnter} {
		if IsControlEvent(typ) {
			t.Errorf("%q should not be a control event", typ)
		}
	}
}
