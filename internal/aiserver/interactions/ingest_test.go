package interactions

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
)

// ---------------------------------------------------------------- harness ---

type harness struct {
	*Service
	db  *store.DB
	now time.Time
	t   *testing.T
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := db.SeedSystemSettings(context.Background()); err != nil {
		t.Fatalf("seed settings: %v", err)
	}

	h := &harness{db: db, now: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC), t: t}
	h.Service = NewService(db)
	h.Service.Now = func() time.Time { return h.now }
	return h
}

func (h *harness) advance(d time.Duration) { h.now = h.now.Add(d) }

// at returns a timestamp offset from the harness clock.
func (h *harness) at(offset time.Duration) time.Time { return h.now.Add(offset) }

func (h *harness) ingest(events ...EventIn) IngestResult {
	h.t.Helper()
	res, err := h.Ingest(context.Background(), events, "test-client")
	if err != nil {
		h.t.Fatalf("ingest: %v", err)
	}
	if len(res.Errors) > 0 {
		h.t.Fatalf("ingest reported errors: %v", res.Errors)
	}
	return res
}

var eventSeq int

func evIn(session, typ, entityType string, entityID int, ts time.Time, meta map[string]any) EventIn {
	eventSeq++
	return EventIn{
		ID:         fmt.Sprintf("evt-%d", eventSeq),
		SessionID:  session,
		TS:         ts,
		Type:       typ,
		EntityType: entityType,
		EntityID:   entityID,
		Metadata:   meta,
	}
}

func (h *harness) segments(session string, sceneID int) []store.SceneWatchSegment {
	h.t.Helper()
	segs, err := h.db.ListSceneSegments(context.Background(), session, sceneID)
	if err != nil {
		h.t.Fatalf("list segments: %v", err)
	}
	return segs
}

func (h *harness) watch(session string, sceneID int) store.SceneWatch {
	h.t.Helper()
	w, err := h.db.GetSceneWatch(context.Background(), session, sceneID)
	if err != nil {
		h.t.Fatalf("get watch: %v", err)
	}
	return w
}

// ------------------------------------------------------------------ tests ---

func TestIngestStoresEventsAndDeduplicates(t *testing.T) {
	h := newHarness(t)

	e1 := evIn("s1", EventSceneView, "scene", 42, h.at(0), nil)
	e2 := evIn("s1", EventScenePageEnter, "scene", 42, h.at(time.Second), nil)

	res := h.ingest(e1, e2)
	if res.Accepted != 2 || res.Duplicates != 0 {
		t.Fatalf("first batch = %+v", res)
	}

	// Re-sending the same events must be recognised, not stored twice.
	res = h.ingest(e1, e2)
	if res.Accepted != 0 || res.Duplicates != 2 {
		t.Errorf("replayed batch = %+v, want 2 duplicates", res)
	}

	// A duplicate inside a single batch is caught too.
	e3 := evIn("s1", EventSceneView, "scene", 42, h.at(2*time.Second), nil)
	res = h.ingest(e3, e3)
	if res.Accepted != 1 || res.Duplicates != 1 {
		t.Errorf("intra-batch duplicate = %+v", res)
	}
}

// Progress events drive state but are never persisted; that is what keeps the
// raw table small under continuous playback.
func TestProgressEventsAreNotPersisted(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(0), m("position", 0.0)),
		evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(5*time.Second), m("position", 5.0)),
		evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(10*time.Second), m("position", 10.0)),
	)

	var stored int
	if err := h.db.SQL().QueryRow(
		`SELECT COUNT(*) FROM interaction_events WHERE event_type = ?`,
		EventSceneWatchProgress).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != 0 {
		t.Errorf("%d progress events were persisted, want 0", stored)
	}

	// They still contributed to the derived segments.
	segs := h.segments("s1", 1)
	if len(segs) != 1 || math.Abs(segs[0].EndS-10) > 1e-6 {
		t.Errorf("segments = %+v, want one ending at 10", segs)
	}
}

func TestSceneWatchSegmentsFromFullSession(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventScenePageEnter, "scene", 1, h.at(0), nil),
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(time.Second), m("position", 0.0, "duration", 600.0)),
		evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(10*time.Second), m("position", 10.0)),
		evIn("s1", EventSceneSeek, "scene", 1, h.at(15*time.Second), m("from", 15.0, "to", 120.0)),
		evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(25*time.Second), m("position", 130.0)),
		// The duration is read from the MOST RECENT duration-bearing event
		// only, so it has to be present here - not just on the start event.
		evIn("s1", EventSceneWatchPause, "scene", 1, h.at(30*time.Second), m("position", 140.0, "duration", 600.0)),
	)

	segs := h.segments("s1", 1)
	if len(segs) != 2 {
		t.Fatalf("segments = %+v, want 2", segs)
	}
	if math.Abs(segs[0].StartS-0) > 1e-6 || math.Abs(segs[0].EndS-15) > 1e-6 {
		t.Errorf("first segment = %v-%v, want 0-15", segs[0].StartS, segs[0].EndS)
	}
	if math.Abs(segs[1].StartS-120) > 1e-6 || math.Abs(segs[1].EndS-140) > 1e-6 {
		t.Errorf("second segment = %v-%v, want 120-140", segs[1].StartS, segs[1].EndS)
	}

	w := h.watch("s1", 1)
	if math.Abs(w.TotalWatchedS-35) > 1e-6 {
		t.Errorf("total watched = %v, want 35", w.TotalWatchedS)
	}
	// 35 of 600 seconds.
	if w.WatchPercent == nil || math.Abs(*w.WatchPercent-35.0/600.0*100) > 1e-6 {
		t.Errorf("watch percent = %v", w.WatchPercent)
	}
}

// The central risk: ingesting overlapping batches must converge on the same
// segments and must not renumber rows that already covered the span.
func TestIncrementalBatchesPreserveRowIDs(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(0), m("position", 0.0)),
		evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(10*time.Second), m("position", 10.0)),
	)
	first := h.segments("s1", 1)
	if len(first) != 1 {
		t.Fatalf("after first batch: %+v", first)
	}
	originalID := first[0].ID

	// A later batch continues the same run.
	h.ingest(
		evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(20*time.Second), m("position", 20.0)),
		evIn("s1", EventSceneWatchPause, "scene", 1, h.at(25*time.Second), m("position", 25.0)),
	)

	second := h.segments("s1", 1)
	if len(second) != 1 {
		t.Fatalf("after second batch: %+v, want the span extended not split", second)
	}
	if second[0].ID != originalID {
		t.Errorf("row id changed %d -> %d; the span should have been reused", originalID, second[0].ID)
	}
	if math.Abs(second[0].EndS-25) > 1e-6 {
		t.Errorf("segment end = %v, want 25", second[0].EndS)
	}
	if math.Abs(second[0].WatchedS-25) > 1e-6 {
		t.Errorf("watched = %v, want 25", second[0].WatchedS)
	}
}

// Re-ingesting a batch already applied must not double-count watch time.
func TestReingestIsIdempotent(t *testing.T) {
	h := newHarness(t)

	events := []EventIn{
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(0), m("position", 0.0)),
		evIn("s1", EventSceneWatchPause, "scene", 1, h.at(30*time.Second), m("position", 30.0)),
	}

	h.ingest(events...)
	before := h.watch("s1", 1)
	beforeSegs := h.segments("s1", 1)

	h.ingest(events...) // exact replay - all duplicates

	after := h.watch("s1", 1)
	afterSegs := h.segments("s1", 1)

	if math.Abs(before.TotalWatchedS-after.TotalWatchedS) > 1e-6 {
		t.Errorf("total watched changed on replay: %v -> %v", before.TotalWatchedS, after.TotalWatchedS)
	}
	if len(beforeSegs) != len(afterSegs) {
		t.Errorf("segment count changed on replay: %d -> %d", len(beforeSegs), len(afterSegs))
	}
	if len(afterSegs) > 0 && beforeSegs[0].ID != afterSegs[0].ID {
		t.Error("replay renumbered a segment")
	}
}

// Two runs separated by a real gap must stay separate.
func TestDisjointRunsStaySeparate(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(0), m("position", 0.0)),
		evIn("s1", EventSceneWatchPause, "scene", 1, h.at(10*time.Second), m("position", 10.0)),
		evIn("s1", EventSceneSeek, "scene", 1, h.at(11*time.Second), m("from", 10.0, "to", 500.0)),
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(12*time.Second), m("position", 500.0)),
		evIn("s1", EventSceneWatchPause, "scene", 1, h.at(20*time.Second), m("position", 520.0)),
	)

	segs := h.segments("s1", 1)
	if len(segs) != 2 {
		t.Fatalf("segments = %+v, want 2 disjoint spans", segs)
	}
	if math.Abs(segs[0].EndS-10) > 1e-6 || math.Abs(segs[1].StartS-500) > 1e-6 {
		t.Errorf("spans = %v-%v and %v-%v", segs[0].StartS, segs[0].EndS, segs[1].StartS, segs[1].EndS)
	}
}

// A progress-only batch can extend the last span, but the heuristic is much
// narrower than it looks: it only applies when the replay produces NO spans of
// its own. Because the replay always reaches back for the most recent control
// event, that requires the opening event to have fallen outside the lookback
// window - which needs several intervening events.
func TestContinuousPlaybackHeuristic(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(0), m("position", 0.0)),
		evIn("s1", EventSceneWatchPause, "scene", 1, h.at(10*time.Second), m("position", 10.0)),
	)
	if segs := h.segments("s1", 1); len(segs) != 1 || math.Abs(segs[0].EndS-10) > 1e-6 {
		t.Fatalf("setup: %+v", segs)
	}

	// Push the opening start beyond the five-event lookback.
	var filler []EventIn
	for i := 0; i < 5; i++ {
		filler = append(filler, evIn("s1", EventSceneView, "scene", 1,
			h.at(time.Duration(11+i)*time.Second), nil))
	}
	h.ingest(filler...)

	// Now a progress-only batch: the replay sees the pause (closing nothing)
	// and the progress, so it yields no spans and the heuristic applies.
	h.ingest(evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(20*time.Second), m("position", 11.5)))

	segs := h.segments("s1", 1)
	if len(segs) != 1 || math.Abs(segs[0].EndS-11.5) > 1e-6 {
		t.Errorf("after small advance = %+v, want the span extended to 11.5", segs)
	}

	// A jump far beyond the tolerance is NOT credited: without a seek event
	// that is not watch time.
	h.ingest(evIn("s1", EventSceneWatchProgress, "scene", 1, h.at(30*time.Second), m("position", 500.0)))
	segs = h.segments("s1", 1)
	if len(segs) != 1 || math.Abs(segs[0].EndS-11.5) > 1e-6 {
		t.Errorf("a large jump was credited: %+v", segs)
	}
}

// When the most recent duration-bearing event carries no duration, no
// percentage is claimed rather than one being guessed.
func TestWatchPercentAbsentWithoutDuration(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventSceneWatchStart, "scene", 1, h.at(0), m("position", 0.0, "duration", 600.0)),
		evIn("s1", EventSceneWatchPause, "scene", 1, h.at(10*time.Second), m("position", 10.0)),
	)

	if w := h.watch("s1", 1); w.WatchPercent != nil {
		t.Errorf("watch percent = %v, want nil when the latest event has no duration", *w.WatchPercent)
	}
}

func TestSessionMergeByFingerprint(t *testing.T) {
	h := newHarness(t)

	h.ingest(evIn("session-a", EventSceneView, "scene", 1, h.at(0), nil))

	// A reload produces a new session id shortly afterwards; it should attach
	// to the existing session rather than starting a fresh one.
	h.advance(30 * time.Second)
	h.ingest(evIn("session-b", EventSceneView, "scene", 2, h.at(0), nil))

	canonical, found, err := h.db.ResolveSessionAlias(context.Background(), "session-b")
	if err != nil {
		t.Fatalf("resolve alias: %v", err)
	}
	if !found || canonical != "session-a" {
		t.Errorf("alias = %q (found %v), want session-a", canonical, found)
	}

	var sessions int
	if err := h.db.SQL().QueryRow(`SELECT COUNT(*) FROM interaction_sessions`).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 1 {
		t.Errorf("%d sessions created, want 1 (the reload should have merged)", sessions)
	}
}

// Past the merge window a new session is started instead.
func TestSessionNotMergedAfterTTL(t *testing.T) {
	h := newHarness(t)

	h.ingest(evIn("session-a", EventSceneView, "scene", 1, h.at(0), nil))

	h.advance(10 * time.Minute) // well past the 120s merge TTL
	h.ingest(evIn("session-b", EventSceneView, "scene", 2, h.at(0), nil))

	if _, found, _ := h.db.ResolveSessionAlias(context.Background(), "session-b"); found {
		t.Error("a session past the merge TTL was still merged")
	}

	var sessions int
	if err := h.db.SQL().QueryRow(`SELECT COUNT(*) FROM interaction_sessions`).Scan(&sessions); err != nil {
		t.Fatalf("count: %v", err)
	}
	if sessions != 2 {
		t.Errorf("%d sessions, want 2", sessions)
	}
}

// A long session that ended on a scene credits it exactly once. This is the
// only path that increments derived_o_count.
func TestStaleSessionCreditsDerivedCount(t *testing.T) {
	h := newHarness(t)

	h.ingest(evIn("long", EventSceneView, "scene", 7, h.at(0), nil))
	// Keep it alive well past the minimum session length.
	h.advance(15 * time.Minute)
	h.ingest(evIn("long", EventSceneWatchProgress, "scene", 7, h.at(0), m("position", 100.0)))

	// Go quiet past the merge TTL, then start something new: that finalises the
	// old session.
	h.advance(10 * time.Minute)
	h.ingest(evIn("fresh", EventSceneView, "scene", 9, h.at(0), nil))

	derived, err := h.db.GetSceneDerived(context.Background(), 7)
	if err != nil {
		t.Fatalf("get derived: %v", err)
	}
	if derived.DerivedOCount != 1 {
		t.Errorf("derived_o_count = %d, want 1", derived.DerivedOCount)
	}

	var endedAt *int64
	if err := h.db.SQL().QueryRow(
		`SELECT ended_at FROM interaction_sessions WHERE session_id = 'long'`).Scan(&endedAt); err != nil {
		t.Fatalf("read session: %v", err)
	}
	if endedAt == nil {
		t.Error("the stale session was not finalised")
	}
}

// A session too short to indicate engagement is closed without credit.
func TestShortSessionIsFinalisedWithoutCredit(t *testing.T) {
	h := newHarness(t)

	h.ingest(evIn("short", EventSceneView, "scene", 7, h.at(0), nil))
	h.advance(2 * time.Minute) // under the 10-minute minimum

	h.advance(10 * time.Minute)
	h.ingest(evIn("fresh", EventSceneView, "scene", 9, h.at(0), nil))

	derived, err := h.db.GetSceneDerived(context.Background(), 7)
	if err != nil {
		t.Fatalf("get derived: %v", err)
	}
	if derived.DerivedOCount != 0 {
		t.Errorf("derived_o_count = %d, want 0 for a short session", derived.DerivedOCount)
	}
}

func TestSceneAndImageViewCounters(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventSceneView, "scene", 1, h.at(0), nil),
		evIn("s1", EventSceneView, "scene", 1, h.at(time.Second), nil),
		evIn("s1", EventImageView, "image", 5, h.at(2*time.Second), nil),
	)

	scene, err := h.db.GetSceneDerived(context.Background(), 1)
	if err != nil {
		t.Fatalf("scene derived: %v", err)
	}
	if scene.ViewCount != 2 {
		t.Errorf("scene view_count = %d, want 2", scene.ViewCount)
	}
	if scene.LastViewedAt == nil {
		t.Error("scene last_viewed_at not set")
	}

	image, err := h.db.GetImageDerived(context.Background(), 5)
	if err != nil {
		t.Fatalf("image derived: %v", err)
	}
	if image.ViewCount != 1 {
		t.Errorf("image view_count = %d, want 1", image.ViewCount)
	}
}

func TestLibrarySearchPersisted(t *testing.T) {
	h := newHarness(t)

	h.ingest(evIn("s1", EventLibrarySearch, "library", 0, h.at(0),
		m("library", "scenes", "query", "beach", "filters", map[string]any{"rating": 4.0})))

	var library, query string
	if err := h.db.SQL().QueryRow(
		`SELECT library, query FROM interaction_library_search`).Scan(&library, &query); err != nil {
		t.Fatalf("read: %v", err)
	}
	if library != "scenes" || query != "beach" {
		t.Errorf("library=%q query=%q", library, query)
	}
}

// "null" arriving as a string must not be stored as a real value.
func TestNullStringsNormalisedInMetadata(t *testing.T) {
	h := newHarness(t)

	h.ingest(evIn("s1", EventSceneView, "scene", 1, h.at(0),
		m("position", 10.0, "note", "null")))

	var meta store.JSONText[map[string]any]
	if err := h.db.SQL().QueryRow(
		`SELECT metadata FROM interaction_events LIMIT 1`).Scan(&meta); err != nil {
		t.Fatalf("read: %v", err)
	}
	if got, present := meta.Data["note"]; !present || got != nil {
		t.Errorf("note = %#v, want nil", got)
	}
	if meta.Data["position"] != 10.0 {
		t.Errorf("real values were altered: %#v", meta.Data)
	}
}

// The page window only ever grows, so an out-of-order batch cannot shrink it.
func TestPageWindowOnlyExpands(t *testing.T) {
	h := newHarness(t)

	h.ingest(
		evIn("s1", EventScenePageEnter, "scene", 1, h.at(10*time.Second), nil),
		evIn("s1", EventScenePageLeave, "scene", 1, h.at(60*time.Second), nil),
	)
	first := h.watch("s1", 1)

	// A late-arriving earlier enter and later leave.
	h.ingest(
		evIn("s1", EventScenePageEnter, "scene", 1, h.at(5*time.Second), nil),
		evIn("s1", EventScenePageLeave, "scene", 1, h.at(90*time.Second), nil),
	)
	second := h.watch("s1", 1)

	if !second.PageEnteredAt.Before(first.PageEnteredAt) {
		t.Errorf("entered_at did not move earlier: %v -> %v", first.PageEnteredAt, second.PageEnteredAt)
	}
	if second.PageLeftAt == nil || !second.PageLeftAt.After(*first.PageLeftAt) {
		t.Errorf("left_at did not move later: %v -> %v", first.PageLeftAt, second.PageLeftAt)
	}
}

func TestEmptyBatch(t *testing.T) {
	h := newHarness(t)
	res, err := h.Ingest(context.Background(), nil, "client")
	if err != nil {
		t.Fatalf("empty ingest: %v", err)
	}
	if res.Accepted != 0 || res.Duplicates != 0 || len(res.Errors) != 0 {
		t.Errorf("empty batch = %+v", res)
	}
}

// An event with no session id is reported rather than silently dropped or
// failing the whole batch.
func TestEventWithoutSessionIsReported(t *testing.T) {
	h := newHarness(t)

	res, err := h.Ingest(context.Background(), []EventIn{
		evIn("", EventSceneView, "scene", 1, h.at(0), nil),
		evIn("s1", EventSceneView, "scene", 2, h.at(time.Second), nil),
	}, "client")
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if res.Accepted != 1 {
		t.Errorf("accepted = %d, want 1 (the valid event)", res.Accepted)
	}
	if len(res.Errors) == 0 {
		t.Error("the invalid event was not reported")
	}
}

// Random overlapping batches must always leave a consistent segment table:
// sorted, non-overlapping, all above the minimum, and total watched time equal
// to the sum of the spans.
func TestSegmentTableInvariantsUnderRandomBatches(t *testing.T) {
	rng := rand.New(rand.NewSource(7))

	for iteration := 0; iteration < 25; iteration++ {
		h := newHarness(t)
		session := "s1"
		sceneID := 1

		pos := 0.0
		for batch := 0; batch < 5; batch++ {
			var events []EventIn
			n := 1 + rng.Intn(4)
			for i := 0; i < n; i++ {
				pos += rng.Float64() * 30
				offset := time.Duration(float64(time.Second) * pos)

				switch rng.Intn(4) {
				case 0:
					events = append(events, evIn(session, EventSceneWatchStart, "scene", sceneID,
						h.at(offset), m("position", pos)))
				case 1:
					events = append(events, evIn(session, EventSceneWatchProgress, "scene", sceneID,
						h.at(offset), m("position", pos)))
				case 2:
					events = append(events, evIn(session, EventSceneWatchPause, "scene", sceneID,
						h.at(offset), m("position", pos)))
				case 3:
					to := pos + rng.Float64()*100
					events = append(events, evIn(session, EventSceneSeek, "scene", sceneID,
						h.at(offset), m("from", pos, "to", to)))
					pos = to
				}
			}

			res, err := h.Ingest(context.Background(), events, "client")
			if err != nil {
				t.Fatalf("iteration %d batch %d: %v", iteration, batch, err)
			}
			if len(res.Errors) > 0 {
				t.Fatalf("iteration %d batch %d errors: %v", iteration, batch, res.Errors)
			}

			segs := h.segments(session, sceneID)
			var total float64
			for i, seg := range segs {
				if seg.EndS < seg.StartS {
					t.Fatalf("iteration %d: inverted segment %+v", iteration, seg)
				}
				if math.Abs(seg.WatchedS-(seg.EndS-seg.StartS)) > 1e-6 {
					t.Fatalf("iteration %d: watched_s inconsistent: %+v", iteration, seg)
				}
				if seg.EndS-seg.StartS < defaultSegmentMinDuration-1e-9 {
					t.Fatalf("iteration %d: sub-minimum segment survived: %+v", iteration, seg)
				}
				if i > 0 && seg.StartS <= segs[i-1].EndS {
					t.Fatalf("iteration %d: overlapping segments %+v", iteration, segs)
				}
				total += seg.WatchedS
			}

			w := h.watch(session, sceneID)
			if math.Abs(w.TotalWatchedS-total) > 1e-6 {
				t.Fatalf("iteration %d: total_watched_s = %v but segments sum to %v",
					iteration, w.TotalWatchedS, total)
			}
		}
	}
}
