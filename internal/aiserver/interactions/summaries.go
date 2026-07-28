package interactions

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// timeValue wraps a time so it can live in a map without pointer aliasing.
type timeValue struct{ t time.Time }

func flattenTimes(in map[int]timeValue) map[int]time.Time {
	out := make(map[int]time.Time, len(in))
	for k, v := range in {
		out[k] = v.t
	}
	return out
}

// pairKey identifies one session's engagement with one scene.
type pairKey struct {
	sessionID string
	sceneID   int
}

// processSceneSummaries derives per-scene watch state from a batch.
//
// Two things happen per (session, scene) pair: the watch row is created or
// extended to cover when the page was open, and - if the batch touched playback
// at all - the watched segments are recomputed and reconciled against what is
// already stored.
func (s *Service) processSceneSummaries(ctx context.Context, events []normalizedEvent, settings Settings) error {
	byPair := map[pairKey][]normalizedEvent{}
	var order []pairKey

	for _, ev := range events {
		if ev.in.EntityType != "scene" || ev.entityID == 0 {
			continue
		}
		key := pairKey{sessionID: ev.sessionID, sceneID: ev.entityID}
		if _, seen := byPair[key]; !seen {
			order = append(order, key)
		}
		byPair[key] = append(byPair[key], ev)
	}
	if len(byPair) == 0 {
		return nil
	}

	// Deterministic order keeps behaviour reproducible under test.
	sort.Slice(order, func(i, j int) bool {
		if order[i].sessionID != order[j].sessionID {
			return order[i].sessionID < order[j].sessionID
		}
		return order[i].sceneID < order[j].sceneID
	})

	viewCounts := map[int]int{}
	lastViewed := map[int]timeValue{}
	touchedScenes := map[int]bool{}

	for _, key := range order {
		pairEvents := byPair[key]
		touchedScenes[key.sceneID] = true

		for _, ev := range pairEvents {
			if ev.in.Type == EventSceneView {
				viewCounts[key.sceneID]++
				if prev, ok := lastViewed[key.sceneID]; !ok || ev.clientTS.After(prev.t) {
					lastViewed[key.sceneID] = timeValue{t: ev.clientTS}
				}
			}
		}

		watch, err := s.upsertSceneWatch(ctx, key, pairEvents)
		if err != nil {
			return fmt.Errorf("scene_watch %s: %w", key.sessionID, err)
		}

		if !touchesPlayback(pairEvents) {
			continue
		}
		if err := s.recomputeSegments(ctx, key, pairEvents, watch, settings); err != nil {
			return fmt.Errorf("summary %s: %w", key.sessionID, err)
		}
	}

	touched := make([]int, 0, len(touchedScenes))
	for id := range touchedScenes {
		touched = append(touched, id)
	}
	sort.Ints(touched)

	return s.db.BumpSceneViews(ctx, viewCounts, flattenTimes(lastViewed), touched)
}

// touchesPlayback reports whether a batch warrants recomputing segments.
func touchesPlayback(events []normalizedEvent) bool {
	for _, ev := range events {
		if watchRelatedTypes[ev.in.Type] {
			return true
		}
	}
	return false
}

// upsertSceneWatch creates or extends the watch row for a pair.
//
// page_entered_at only ever moves earlier and page_left_at only later, so a
// batch arriving out of order cannot shrink a known-open window.
func (s *Service) upsertSceneWatch(ctx context.Context, key pairKey, events []normalizedEvent) (store.SceneWatch, error) {
	watch, err := s.db.GetSceneWatch(ctx, key.sessionID, key.sceneID)
	exists := err == nil
	if err != nil && !errors.Is(err, store.ErrWatchNotFound) {
		return store.SceneWatch{}, err
	}

	var entered, left *time.Time
	for _, ev := range events {
		switch ev.in.Type {
		case EventScenePageEnter, EventSceneView:
			if entered == nil || ev.clientTS.Before(*entered) {
				ts := ev.clientTS
				entered = &ts
			}
		case EventScenePageLeave:
			if left == nil || ev.clientTS.After(*left) {
				ts := ev.clientTS
				left = &ts
			}
		}
	}

	if !exists {
		watch = store.SceneWatch{SessionID: key.sessionID, SceneID: key.sceneID}
		if entered != nil {
			watch.PageEnteredAt = *entered
		} else {
			// No explicit enter event: the earliest thing seen for this pair is
			// the best available answer.
			earliest := events[0].clientTS
			for _, ev := range events[1:] {
				if ev.clientTS.Before(earliest) {
					earliest = ev.clientTS
				}
			}
			watch.PageEnteredAt = earliest
		}
	} else if entered != nil && entered.Before(watch.PageEnteredAt) {
		watch.PageEnteredAt = *entered
	}

	if left != nil && (watch.PageLeftAt == nil || left.After(*watch.PageLeftAt)) {
		watch.PageLeftAt = left
	}

	// No explicit leave: infer one only if the session has clearly moved on to
	// something else. A page still being viewed must stay open, or its watch
	// percentage would be computed against a truncated window.
	if watch.PageLeftAt == nil {
		if inferred := s.inferPageLeft(ctx, key, watch.PageEnteredAt); inferred != nil {
			watch.PageLeftAt = inferred
		}
	}

	if exists {
		return watch, s.db.UpdateSceneWatch(ctx, watch)
	}
	id, err := s.db.CreateSceneWatch(ctx, watch)
	if err != nil {
		return store.SceneWatch{}, err
	}
	watch.ID = id
	return watch, nil
}

// inferPageLeft returns when the user probably left a scene page, or nil if
// they appear still to be on it.
func (s *Service) inferPageLeft(ctx context.Context, key pairKey, enteredAt time.Time) *time.Time {
	session, err := s.db.GetSession(ctx, key.sessionID)
	if err != nil {
		return nil
	}

	// Still on this scene: the page is open, so there is no leave time.
	if session.LastEntityType != nil && *session.LastEntityType == "scene" &&
		session.LastEntityID != nil && *session.LastEntityID == key.sceneID {
		return nil
	}

	candidate := session.LastEntityEventTS
	if candidate == nil {
		candidate = &session.LastEventTS
	}
	if candidate.Before(enteredAt) {
		return nil
	}
	ts := *candidate
	return &ts
}

// recomputeSegments replays a window of events and reconciles the result with
// the stored segments.
func (s *Service) recomputeSegments(ctx context.Context, key pairKey, events []normalizedEvent, watch store.SceneWatch, settings Settings) error {
	batchMin, batchMax := events[0].clientTS, events[0].clientTS
	for _, ev := range events[1:] {
		if ev.clientTS.Before(batchMin) {
			batchMin = ev.clientTS
		}
		if ev.clientTS.After(batchMax) {
			batchMax = ev.clientTS
		}
	}

	from := batchMin.Add(-settings.SegmentTimeMargin)
	to := batchMax.Add(settings.SegmentTimeMargin)

	window, err := s.db.LoadSceneEventWindow(ctx, key.sessionID, key.sceneID, from, to, beforeEventLimit)
	if err != nil {
		return err
	}

	replay := buildReplay(window, events)
	computed := ComputeSegments(replay, settings.SegmentMergeGap, settings.SegmentMinDuration)

	existing, err := s.db.ListSceneSegments(ctx, key.sessionID, key.sceneID)
	if err != nil {
		return err
	}

	final, err := s.reconcileSegments(ctx, key, watch, existing, computed, settings)
	if err != nil {
		return err
	}

	// Continuous playback: a batch of nothing but progress events produces no
	// new spans of its own, because it never opened or closed a run. Extend the
	// last known span instead - but only by a plausible amount, so a position
	// jump that should have been a seek does not silently invent watch time.
	if len(computed) == 0 && hasProgressOnly(events) && len(final) > 0 {
		if maxPos, ok := maxProgressPosition(events); ok {
			last := &final[len(final)-1]
			limit := last.EndS + settings.SegmentMergeGap*4
			if maxPos > last.EndS && maxPos <= limit {
				last.EndS = maxPos
				last.WatchedS = last.EndS - last.StartS
				if err := s.db.UpdateSceneSegment(ctx, *last); err != nil {
					return err
				}
			}
		}
	}

	return s.updateWatchStats(ctx, key, watch, final)
}

// buildReplay assembles the event sequence to replay: stored context around the
// window, plus this batch's progress events, which are never persisted and so
// must be injected synthetically.
func buildReplay(window store.SceneEventWindow, batch []normalizedEvent) []ReplayEvent {
	var out []ReplayEvent

	// Before rows arrive newest-first; replay needs chronological order.
	for i := len(window.Before) - 1; i >= 0; i-- {
		e := window.Before[i]
		out = append(out, ReplayEvent{Type: e.EventType, ClientTS: e.ClientTS, Metadata: e.Metadata})
	}
	for _, e := range window.Within {
		out = append(out, ReplayEvent{Type: e.EventType, ClientTS: e.ClientTS, Metadata: e.Metadata})
	}
	if window.After != nil {
		out = append(out, ReplayEvent{
			Type: window.After.EventType, ClientTS: window.After.ClientTS, Metadata: window.After.Metadata,
		})
	}

	for _, ev := range batch {
		if ev.in.Type != EventSceneWatchProgress {
			continue
		}
		out = append(out, ReplayEvent{Type: ev.in.Type, ClientTS: ev.clientTS, Metadata: ev.metadata})
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].ClientTS.Before(out[j].ClientTS) })
	return out
}

func hasProgressOnly(events []normalizedEvent) bool {
	sawProgress := false
	for _, ev := range events {
		if IsControlEvent(ev.in.Type) {
			return false
		}
		if ev.in.Type == EventSceneWatchProgress {
			sawProgress = true
		}
	}
	return sawProgress
}

func maxProgressPosition(events []normalizedEvent) (float64, bool) {
	var best float64
	found := false
	for _, ev := range events {
		if ev.in.Type != EventSceneWatchProgress {
			continue
		}
		if pos, ok := metaFloat(ev.metadata, "position"); ok && (!found || pos > best) {
			best, found = pos, true
		}
	}
	return best, found
}

// reconcileSegments merges newly computed spans into the stored ones.
//
// The union of old and new is merged and filtered, then written back so that
// existing rows are REUSED wherever an interval overlaps one. Preserving row
// ids matters because segments are referenced by id elsewhere and because
// churning them would make the table's history meaningless - the alternative,
// delete-all-and-reinsert, would renumber every segment on every progress
// event.
func (s *Service) reconcileSegments(ctx context.Context, key pairKey, watch store.SceneWatch, existing []store.SceneWatchSegment, computed []Interval, settings Settings) ([]store.SceneWatchSegment, error) {
	union := make([]Interval, 0, len(existing)+len(computed))
	for _, seg := range existing {
		union = append(union, Interval{Start: seg.StartS, End: seg.EndS})
	}
	union = append(union, computed...)

	target := FilterIntervals(MergeIntervals(union, settings.SegmentMergeGap), settings.SegmentMinDuration)

	claimed := map[int64]bool{}
	toDelete := map[int64]bool{}
	var final []store.SceneWatchSegment

	for _, iv := range target {
		// Overlap is judged with the merge gap applied, so a row that merely
		// abuts the interval still counts as the same span.
		var best *store.SceneWatchSegment
		var bestOverlap float64
		var overlapping []*store.SceneWatchSegment

		for i := range existing {
			seg := &existing[i]
			if claimed[seg.ID] {
				continue
			}
			if seg.EndS < iv.Start-settings.SegmentMergeGap || seg.StartS > iv.End+settings.SegmentMergeGap {
				continue
			}
			overlapping = append(overlapping, seg)

			overlap := min(seg.EndS, iv.End) - max(seg.StartS, iv.Start)
			if overlap < 0 {
				overlap = 0
			}
			if best == nil || overlap > bestOverlap {
				best, bestOverlap = seg, overlap
			}
		}

		if best == nil {
			id, err := s.db.InsertSceneSegment(ctx, store.SceneWatchSegment{
				SceneWatchID: watch.ID,
				SessionID:    key.sessionID,
				SceneID:      key.sceneID,
				StartS:       iv.Start,
				EndS:         iv.End,
				WatchedS:     iv.Duration(),
			})
			if err != nil {
				return nil, err
			}
			final = append(final, store.SceneWatchSegment{
				ID: id, SceneWatchID: watch.ID, SessionID: key.sessionID, SceneID: key.sceneID,
				StartS: iv.Start, EndS: iv.End, WatchedS: iv.Duration(),
			})
			continue
		}

		// Expand the chosen row to the merged interval and retire the rest.
		best.StartS = min(best.StartS, iv.Start)
		best.EndS = max(best.EndS, iv.End)
		best.WatchedS = best.EndS - best.StartS
		claimed[best.ID] = true

		for _, other := range overlapping {
			if other.ID != best.ID {
				toDelete[other.ID] = true
			}
		}

		if err := s.db.UpdateSceneSegment(ctx, *best); err != nil {
			return nil, err
		}
		final = append(final, *best)
	}

	// Any stored row not claimed by an interval no longer belongs: it was
	// either absorbed into one, or was too short to survive filtering.
	for i := range existing {
		seg := existing[i]
		if !claimed[seg.ID] {
			toDelete[seg.ID] = true
		}
	}

	if len(toDelete) > 0 {
		ids := make([]int64, 0, len(toDelete))
		for id := range toDelete {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if err := s.db.DeleteSceneSegments(ctx, ids); err != nil {
			return nil, err
		}
	}

	sort.Slice(final, func(i, j int) bool { return final[i].StartS < final[j].StartS })
	return final, nil
}

// watchDurationTypes carry the video's duration in their metadata.
var watchDurationTypes = []string{
	EventSceneWatchComplete, EventSceneWatchPause, EventSceneWatchProgress, EventSceneWatchStart,
}

// updateWatchStats recomputes total watched time and the completion percentage.
func (s *Service) updateWatchStats(ctx context.Context, key pairKey, watch store.SceneWatch, segments []store.SceneWatchSegment) error {
	var total float64
	for _, seg := range segments {
		total += seg.WatchedS
	}
	watch.TotalWatchedS = total

	// The player reports the video's duration on several event types; the most
	// recent one is the most trustworthy.
	var duration float64
	if ev, err := s.db.LatestSceneEventWithTypes(ctx, key.sessionID, key.sceneID, watchDurationTypes); err == nil && ev != nil {
		if d, ok := metaFloat(ev.Metadata, "duration"); ok && d > 0 {
			duration = d
		}
	}

	// Failing that, how long the page was open is a rough proxy.
	if duration == 0 && watch.PageLeftAt != nil {
		if d := watch.PageLeftAt.Sub(watch.PageEnteredAt).Seconds(); d > 0 {
			duration = d
		}
	}

	// Only claim a percentage when the denominator means something.
	if duration > 0 {
		percent := total / duration * 100
		if percent > 100 {
			percent = 100
		}
		watch.WatchPercent = &percent
	}

	return s.db.UpdateSceneWatch(ctx, watch)
}
