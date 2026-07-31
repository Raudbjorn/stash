// Package interactions ingests the watch-tracking events the Stash UI plugin
// emits, and derives per-scene watch segments and view statistics from them.
//
// This is the most intricate part of the port. The frontend reports player
// events (start, pause, seek, progress, complete) with positions in the video,
// and the server reconstructs which parts of a scene were actually watched.
// Events arrive in batches, out of order relative to what is already stored,
// and may overlap previously computed segments.
package interactions

import (
	"sort"
	"time"
)

// Event types the player emits.
const (
	EventSceneWatchStart    = "scene_watch_start"
	EventSceneWatchPause    = "scene_watch_pause"
	EventSceneWatchComplete = "scene_watch_complete"
	EventSceneWatchProgress = "scene_watch_progress"
	EventSceneSeek          = "scene_seek"
	EventScenePageEnter     = "scene_page_enter"
	EventScenePageLeave     = "scene_page_leave"
	EventSceneView          = "scene_view"
	EventImageView          = "image_view"
	EventLibrarySearch      = "library_search"
)

// controlEventTypes are the events that change playback state, as opposed to
// merely reporting a position.
//
// The distinction matters twice: the replay window reaches back for one of
// these to recover playback state, and the continuous-playback heuristic only
// applies when a batch contains none.
var controlEventTypes = map[string]bool{
	EventSceneWatchStart:    true,
	EventSceneWatchPause:    true,
	EventSceneWatchComplete: true,
	EventSceneSeek:          true,
}

// IsControlEvent reports whether an event type changes playback state.
func IsControlEvent(t string) bool { return controlEventTypes[t] }

// watchRelatedTypes trigger segment recomputation for a (session, scene) pair.
var watchRelatedTypes = map[string]bool{
	EventSceneWatchStart:    true,
	EventSceneWatchPause:    true,
	EventSceneWatchComplete: true,
	EventSceneWatchProgress: true,
	EventSceneSeek:          true,
}

// ReplayEvent is one event fed to the segment state machine.
//
// It deliberately carries only what the machine reads, so stored rows and
// synthetic in-batch events can both be replayed through the same code.
type ReplayEvent struct {
	Type     string
	ClientTS time.Time
	Metadata map[string]any
}

// Interval is a half-open watched span in seconds within a scene.
type Interval struct {
	Start float64
	End   float64
}

// Duration is the span's length.
func (i Interval) Duration() float64 {
	if i.End <= i.Start {
		return 0
	}
	return i.End - i.Start
}

// ComputeSegments replays player events into watched intervals.
//
// The machine tracks two positions: where the current play run began, and the
// most recently reported position. A run is closed - emitting an interval -
// whenever playback stops or jumps.
//
//	start:    open a run at metadata.position, else the last known position, else 0
//	seek:     if playing, close at metadata.from (else last position); if
//	          metadata.to is present move there and resume only if it was
//	          playing; with no `to`, stop tracking a run
//	pause,
//	complete: close at metadata.position, else last position, else the run start
//	progress: update the last position, and if no run is open treat this as
//	          the start of one (playback that began before the window)
//
// After replay any still-open run is flushed, then intervals are merged when
// they are within mergeGap of each other and dropped when shorter than
// minDuration.
//
// A degenerate run - one whose end is not beyond its start - emits nothing;
// that is what makes repeated pauses at the same position harmless.
func ComputeSegments(events []ReplayEvent, mergeGap, minDuration float64) []Interval {
	var (
		segments     []Interval
		playStartPos *float64
		lastPos      *float64
	)

	closeSegment := func(endPos float64) {
		if playStartPos == nil {
			return
		}
		if endPos > *playStartPos {
			segments = append(segments, Interval{Start: *playStartPos, End: endPos})
		}
		playStartPos = nil
		end := endPos
		lastPos = &end
	}

	for _, ev := range events {
		switch ev.Type {
		case EventSceneWatchStart:
			var start float64
			if pos, ok := metaFloat(ev.Metadata, "position"); ok {
				start = pos
			} else if lastPos != nil {
				start = *lastPos
			}
			s := start
			playStartPos = &s
			l := start
			lastPos = &l

		case EventSceneSeek:
			wasPlaying := playStartPos != nil

			if wasPlaying {
				// Close at where the seek departed from.
				end := *playStartPos
				if from, ok := metaFloat(ev.Metadata, "from"); ok {
					end = from
				} else if lastPos != nil {
					end = *lastPos
				}
				closeSegment(end)
			}

			if to, ok := metaFloat(ev.Metadata, "to"); ok {
				t := to
				lastPos = &t
				if wasPlaying {
					// Resume at the destination only if playback was running.
					r := to
					playStartPos = &r
				} else {
					playStartPos = nil
				}
			} else {
				// A seek with no destination leaves position unknown.
				playStartPos = nil
			}

		case EventSceneWatchPause, EventSceneWatchComplete:
			pos, ok := metaFloat(ev.Metadata, "position")
			if !ok && lastPos != nil {
				pos, ok = *lastPos, true
			}
			if !ok && playStartPos != nil {
				pos, ok = *playStartPos, true
			}
			if ok {
				closeSegment(pos)
			}

		case EventSceneWatchProgress:
			if pos, ok := metaFloat(ev.Metadata, "position"); ok {
				p := pos
				lastPos = &p
				if playStartPos == nil {
					// Playback was already running before this window; treat
					// the first position seen as the start of a run.
					s := pos
					playStartPos = &s
				}
			}
		}
	}

	// Flush a run still open at the end of the replay.
	if playStartPos != nil {
		end := *playStartPos
		if lastPos != nil {
			end = *lastPos
		}
		if end > *playStartPos {
			segments = append(segments, Interval{Start: *playStartPos, End: end})
		}
	}

	return FilterIntervals(MergeIntervals(segments, mergeGap), minDuration)
}

// MergeIntervals coalesces intervals that are within gap of one another.
// The input need not be sorted; the result is sorted by start.
func MergeIntervals(in []Interval, gap float64) []Interval {
	if len(in) == 0 {
		return nil
	}

	sorted := make([]Interval, len(in))
	copy(sorted, in)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})

	merged := []Interval{sorted[0]}
	for _, iv := range sorted[1:] {
		last := &merged[len(merged)-1]
		if iv.Start <= last.End+gap {
			if iv.End > last.End {
				last.End = iv.End
			}
			continue
		}
		merged = append(merged, iv)
	}
	return merged
}

// FilterIntervals drops intervals shorter than minDuration.
func FilterIntervals(in []Interval, minDuration float64) []Interval {
	if minDuration < 0 {
		minDuration = 0
	}
	out := make([]Interval, 0, len(in))
	for _, iv := range in {
		if iv.Duration() >= minDuration {
			out = append(out, iv)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// TotalWatched sums the intervals' durations.
func TotalWatched(in []Interval) float64 {
	var total float64
	for _, iv := range in {
		total += iv.Duration()
	}
	return total
}

// metaFloat reads a numeric metadata field.
//
// The frontend is JavaScript, so a position may arrive as a JSON number or, if
// it went through a string conversion somewhere, as a string. Both are
// accepted; anything else is treated as absent rather than as zero, because
// zero is a meaningful position.
func metaFloat(meta map[string]any, key string) (float64, bool) {
	if meta == nil {
		return 0, false
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return 0, false
	}
	return toFloat(raw)
}
