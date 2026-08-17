package store

import (
	"context"
	"math"
	"reflect"
	"testing"
)

func fptr(f float64) *float64 { return &f }
func iptr(i int) *int         { return &i }

// resolveAll maps labels to tag ids from a table, returning nil for anything
// absent - which is how an unresolved label is expressed.
func resolveAll(table map[string]int) func(label, category string) *int {
	return func(label, category string) *int {
		if id, ok := table[label]; ok {
			return &id
		}
		return nil
	}
}

func sceneRun(tagTable map[string]int) SceneRunInput {
	return SceneRunInput{
		Service: "AI_Tagging",
		SceneID: 42,
		Models: []ModelInput{{
			ModelID: iptr(950), Name: "vivid_galaxy", Version: fptr(1.9),
			Type: sptr("tagging"), Categories: []string{"actions"},
		}},
		FrameInterval:    fptr(2.0),
		Duration:         fptr(600),
		ResolveReference: resolveAll(tagTable),
	}
}

func sptr(s string) *string { return &s }

// A detection that matched one sampled frame covers the interval up to the next
// sample, not an instant. Getting this wrong makes every span zero-length.
func TestSceneTimespanEndIncludesFrameInterval(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(map[string]int{"Kissing": 7})
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {
			// A single-frame detection at t=10 with no explicit end.
			"Kissing": {{Start: 10}},
		},
	}

	runID, err := db.StoreSceneRun(ctx, in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	var start, end float64
	if err := db.SQL().QueryRow(
		`SELECT start_s, end_s FROM ai_result_timespans WHERE run_id = ?`, runID).
		Scan(&start, &end); err != nil {
		t.Fatalf("read: %v", err)
	}
	if start != 10 || end != 12 {
		t.Errorf("timespan = %v-%v, want 10-12 (start + the 2s frame interval)", start, end)
	}

	// An explicit end is extended the same way.
	in2 := sceneRun(map[string]int{"Kissing": 7})
	in2.SceneID = 43
	in2.Timespans = map[string]map[string][]FrameDetection{
		"actions": {"Kissing": {{Start: 10, End: fptr(20)}}},
	}
	runID2, err := db.StoreSceneRun(ctx, in2)
	if err != nil {
		t.Fatalf("store 2: %v", err)
	}
	if err := db.SQL().QueryRow(
		`SELECT start_s, end_s FROM ai_result_timespans WHERE run_id = ?`, runID2).
		Scan(&start, &end); err != nil {
		t.Fatalf("read 2: %v", err)
	}
	if start != 10 || end != 22 {
		t.Errorf("timespan = %v-%v, want 10-22", start, end)
	}
}

// A run with no reported frame interval falls back to the default rather than
// collapsing every span.
func TestFrameIntervalDefault(t *testing.T) {
	db := openTestDB(t)

	in := sceneRun(map[string]int{"Kissing": 7})
	in.FrameInterval = nil
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {"Kissing": {{Start: 0}}},
	}

	runID, err := db.StoreSceneRun(context.Background(), in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	var end float64
	if err := db.SQL().QueryRow(
		`SELECT end_s FROM ai_result_timespans WHERE run_id = ?`, runID).Scan(&end); err != nil {
		t.Fatalf("read: %v", err)
	}
	if end != defaultFrameInterval {
		t.Errorf("end = %v, want the default interval %v", end, defaultFrameInterval)
	}
}

// An unresolved label still produces timespans - the raw record - but no
// aggregate, because an aggregate with no tag id could never be joined.
func TestUnresolvedLabelsGetTimespansButNoAggregates(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(map[string]int{"Kissing": 7}) // "Unknown" deliberately absent
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {
			"Kissing": {{Start: 0}, {Start: 10}},
			"Unknown": {{Start: 20}},
		},
	}

	runID, err := db.StoreSceneRun(ctx, in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	var timespans, resolved, aggregates int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM ai_result_timespans WHERE run_id = ?`, runID).Scan(&timespans); err != nil {
		t.Fatalf("count timespans: %v", err)
	}
	if timespans != 3 {
		t.Errorf("%d timespans, want 3 including the unresolved label", timespans)
	}

	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM ai_result_timespans WHERE run_id = ? AND value_id IS NOT NULL`,
		runID).Scan(&resolved); err != nil {
		t.Fatalf("count resolved: %v", err)
	}
	if resolved != 2 {
		t.Errorf("%d resolved timespans, want 2", resolved)
	}

	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM ai_result_aggregates WHERE run_id = ?`, runID).Scan(&aggregates); err != nil {
		t.Fatalf("count aggregates: %v", err)
	}
	if aggregates != 1 {
		t.Errorf("%d aggregates, want 1 (only the resolved label)", aggregates)
	}
}

func TestSceneTagTotals(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(map[string]int{"Kissing": 7, "Blowjob": 9})
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {
			// Two 2-second spans, then one.
			"Kissing": {{Start: 0}, {Start: 10}},
			"Blowjob": {{Start: 30}},
		},
	}
	if _, err := db.StoreSceneRun(ctx, in); err != nil {
		t.Fatalf("store: %v", err)
	}

	totals, err := db.GetSceneTagTotals(ctx, "AI_Tagging", 42)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if math.Abs(totals[7]-4) > 1e-9 {
		t.Errorf("tag 7 total = %v, want 4", totals[7])
	}
	if math.Abs(totals[9]-2) > 1e-9 {
		t.Errorf("tag 9 total = %v, want 2", totals[9])
	}

	// Totals sum across runs, so re-analysis accumulates.
	if _, err := db.StoreSceneRun(ctx, in); err != nil {
		t.Fatalf("second store: %v", err)
	}
	totals, err = db.GetSceneTagTotals(ctx, "AI_Tagging", 42)
	if err != nil {
		t.Fatalf("totals: %v", err)
	}
	if math.Abs(totals[7]-8) > 1e-9 {
		t.Errorf("tag 7 total after a second run = %v, want 8", totals[7])
	}
}

func TestGetSceneTimespansBuckets(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(map[string]int{"Kissing": 7})
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {
			"Kissing": {
				{Start: 10, Extra: map[string]any{"confidence": 0.9}},
				{Start: 0, Extra: map[string]any{"confidence": 0.5}},
			},
		},
	}
	if _, err := db.StoreSceneRun(ctx, in); err != nil {
		t.Fatalf("store: %v", err)
	}

	buckets, err := db.GetSceneTimespans(ctx, "AI_Tagging", 42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	spans := buckets["actions"]["7"] // keyed by tag id, not label
	if len(spans) != 2 {
		t.Fatalf("spans = %+v, want 2", spans)
	}
	// Ordered by start so callers can merge directly.
	if spans[0].Start != 0 || spans[1].Start != 10 {
		t.Errorf("spans not ordered by start: %+v", spans)
	}
	if spans[0].Confidence == nil || math.Abs(*spans[0].Confidence-0.5) > 1e-9 {
		t.Errorf("confidence = %v", spans[0].Confidence)
	}

	// A scene with nothing stored returns nil, not an empty map.
	empty, err := db.GetSceneTimespans(ctx, "AI_Tagging", 999)
	if err != nil {
		t.Fatalf("get empty: %v", err)
	}
	if empty != nil {
		t.Errorf("expected nil for a scene with no results, got %v", empty)
	}
}

// An unfinished run must never outrank a completed one, which is why the
// ordering uses (completed_at IS NULL) rather than NULLS LAST.
func TestLatestSceneRunOrdersNullsLast(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(map[string]int{"Kissing": 7})
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {"Kissing": {{Start: 0}}},
	}
	completed, err := db.StoreSceneRun(ctx, in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	// A later run that never completed.
	if _, err := db.SQL().Exec(
		`INSERT INTO ai_model_runs (service, entity_type, entity_id, status, started_at, completed_at)
		 VALUES ('AI_Tagging','scene',42,'running',?,NULL)`, NowMillis()); err != nil {
		t.Fatalf("insert running: %v", err)
	}

	run, err := db.GetLatestSceneRun(ctx, "AI_Tagging", 42)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if run == nil {
		t.Fatal("no run returned")
	}
	if run.RunID != completed {
		t.Errorf("latest run = %d, want the completed one (%d)", run.RunID, completed)
	}
	if run.CompletedAt == nil {
		t.Error("completed_at not reported")
	}
	if len(run.Models) == 0 {
		t.Error("model history not reported")
	}

	// Aggregates are keyed "category:tagID".
	if _, ok := run.Aggregates["actions:7"]; !ok {
		t.Errorf("aggregates = %v, want a key actions:7", run.Aggregates)
	}
}

func TestLatestSceneRunAbsent(t *testing.T) {
	db := openTestDB(t)
	run, err := db.GetLatestSceneRun(context.Background(), "AI_Tagging", 12345)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if run != nil {
		t.Errorf("expected nil for a scene with no runs, got %+v", run)
	}
}

// Re-analysing an image must replace its results for the same categories, or a
// tag the model no longer detects would linger forever.
func TestImageRunClearsStaleCategories(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	first := ImageRunInput{
		Service: "AI_Tagging", ImageID: 5,
		TagsByCategory: map[string][]int{"actions": {1, 2, 3}},
	}
	if _, err := db.StoreImageRun(ctx, first); err != nil {
		t.Fatalf("first run: %v", err)
	}

	tags, err := db.GetImageTagIDs(ctx, "AI_Tagging", 5)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 3 {
		t.Fatalf("tags = %v, want 3", tags)
	}

	// A second run detects fewer tags in the same category.
	second := ImageRunInput{
		Service: "AI_Tagging", ImageID: 5,
		TagsByCategory: map[string][]int{"actions": {1}},
	}
	if _, err := db.StoreImageRun(ctx, second); err != nil {
		t.Fatalf("second run: %v", err)
	}

	tags, err = db.GetImageTagIDs(ctx, "AI_Tagging", 5)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 1 || tags[0] != 1 {
		t.Errorf("tags = %v, want only [1]; stale results were not cleared", tags)
	}
}

// The empty-category case needs its own statement because equality never
// matches NULL.
func TestImageRunClearsNullCategory(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.StoreImageRun(ctx, ImageRunInput{
		Service: "AI_Tagging", ImageID: 6,
		TagsByCategory: map[string][]int{"": {10, 11}},
	}); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := db.StoreImageRun(ctx, ImageRunInput{
		Service: "AI_Tagging", ImageID: 6,
		TagsByCategory: map[string][]int{"": {10}},
	}); err != nil {
		t.Fatalf("second: %v", err)
	}

	tags, err := db.GetImageTagIDs(ctx, "AI_Tagging", 6)
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 1 {
		t.Errorf("tags = %v, want 1; the NULL-category branch did not clear", tags)
	}
}

// Models are keyed by (service, model_id, name) and updated in place, so
// repeated runs do not accumulate duplicates.
func TestModelUpsert(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(nil)
	in.Timespans = map[string]map[string][]FrameDetection{}

	for i := 0; i < 3; i++ {
		if _, err := db.StoreSceneRun(ctx, in); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	var models int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM ai_models`).Scan(&models); err != nil {
		t.Fatalf("count: %v", err)
	}
	if models != 1 {
		t.Errorf("%d model rows after three runs, want 1", models)
	}

	// A model with no numeric id gets its own row rather than colliding.
	noID := sceneRun(nil)
	noID.Timespans = map[string]map[string][]FrameDetection{}
	noID.Models = []ModelInput{{Name: "vivid_galaxy"}}
	if _, err := db.StoreSceneRun(ctx, noID); err != nil {
		t.Fatalf("no-id run: %v", err)
	}
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM ai_models`).Scan(&models); err != nil {
		t.Fatalf("count: %v", err)
	}
	if models != 2 {
		t.Errorf("%d model rows, want 2 (NULL model_id is a distinct key)", models)
	}
}

func TestPurgeSceneCategories(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(map[string]int{"Kissing": 7, "Talking": 8})
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {"Kissing": {{Start: 0}}},
		"speech":  {"Talking": {{Start: 5}}},
	}
	if _, err := db.StoreSceneRun(ctx, in); err != nil {
		t.Fatalf("store: %v", err)
	}

	if err := db.PurgeSceneCategories(ctx, "AI_Tagging", 42, []string{"speech"}, nil); err != nil {
		t.Fatalf("purge: %v", err)
	}

	buckets, err := db.GetSceneTimespans(ctx, "AI_Tagging", 42)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if _, present := buckets["speech"]; present {
		t.Error("purged category still present")
	}
	if _, present := buckets["actions"]; !present {
		t.Error("purge removed the wrong category")
	}

	// An empty category list is a no-op rather than a wipe.
	if err := db.PurgeSceneCategories(ctx, "AI_Tagging", 42, nil, nil); err != nil {
		t.Fatalf("empty purge: %v", err)
	}
	if buckets, _ := db.GetSceneTimespans(ctx, "AI_Tagging", 42); len(buckets) == 0 {
		t.Error("an empty purge deleted everything")
	}
}

// Cascades are declared in DDL but not enforced by this engine, so deletion is
// explicit; this verifies nothing is orphaned.
func TestDeleteRunRemovesChildren(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	in := sceneRun(map[string]int{"Kissing": 7})
	in.Timespans = map[string]map[string][]FrameDetection{
		"actions": {"Kissing": {{Start: 0}}},
	}
	runID, err := db.StoreSceneRun(ctx, in)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	if err := db.DeleteRun(ctx, runID); err != nil {
		t.Fatalf("delete: %v", err)
	}

	for _, table := range []string{
		"ai_model_runs", "ai_result_timespans", "ai_result_aggregates", "ai_model_run_models",
	} {
		var n int
		q := `SELECT COUNT(*) FROM ` + table
		if table != "ai_model_runs" {
			q += ` WHERE run_id = ` + itoa(runID)
		} else {
			q += ` WHERE id = ` + itoa(runID)
		}
		if err := db.SQL().QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%s left %d orphaned rows", table, n)
		}
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for v > 0 {
		pos--
		b[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(b[pos:])
}

// The provider's own label must be stored, not only the tag it resolved to.
//
// A5 established that an unresolved label still gets timespans "as the raw
// record". That is only true if the label is actually written: without it the
// row records a time range and nothing about what was detected, so the raw
// record it is supposed to leave behind is unreadable and a user cannot see
// which tag they would need to create.
func TestUnresolvedLabelsKeepTheirName(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	end := 10.0
	_, err := db.StoreSceneRun(ctx, SceneRunInput{
		Service: "native",
		SceneID: 7,
		Timespans: map[string]map[string][]FrameDetection{
			"actions": {
				"Resolved":   {{Start: 0, End: &end}},
				"Unresolved": {{Start: 20, End: &end}},
			},
		},
		ResolveReference: func(label, category string) *int {
			if label == "Resolved" {
				id := 42
				return &id
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("StoreSceneRun: %v", err)
	}

	spans, err := db.GetSceneSpansByLabel(ctx, "native", 7, 0)
	if err != nil {
		t.Fatalf("GetSceneSpansByLabel: %v", err)
	}

	actions, ok := spans["actions"]
	if !ok {
		t.Fatalf("no actions category: %+v", spans)
	}

	// Both labels must be present and named.
	for _, label := range []string{"Resolved", "Unresolved"} {
		if len(actions[label]) == 0 {
			t.Errorf("label %q has no spans; it was stored without its name", label)
		}
	}

	// The resolved one carries its tag id; the unresolved one does not, which
	// is what tells a UI a tag is missing rather than that nothing was found.
	if actions["Resolved"][0].TagID == nil || *actions["Resolved"][0].TagID != 42 {
		t.Errorf("resolved span TagID = %v, want 42", actions["Resolved"][0].TagID)
	}
	if actions["Unresolved"][0].TagID != nil {
		t.Errorf("unresolved span carries TagID %v", *actions["Unresolved"][0].TagID)
	}
}

// An unknown scene must yield an empty map, not nil: the caller serialises it
// to a UI that maps over the result, and null would crash it.
func TestSpansByLabelIsNeverNil(t *testing.T) {
	db := openTestDB(t)

	spans, err := db.GetSceneSpansByLabel(context.Background(), "native", 999, 0)
	if err != nil {
		t.Fatal(err)
	}
	if spans == nil {
		t.Fatal("GetSceneSpansByLabel returned nil for an unknown scene")
	}
	if len(spans) != 0 {
		t.Errorf("spans = %+v, want empty", spans)
	}
}

// Reading spans across every run would feed marker regeneration one overlapping
// copy per analysis, so a scene analysed three times would regenerate to
// different boundaries than it produced the first time.
func TestSpansCanBeRestrictedToOneRun(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	end := 10.0
	input := SceneRunInput{
		Service:   "native",
		SceneID:   3,
		Timespans: map[string]map[string][]FrameDetection{"actions": {"Blowjob": {{Start: 0, End: &end}}}},
	}

	first, err := db.StoreSceneRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.StoreSceneRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}

	all, err := db.GetSceneSpansByLabel(ctx, "native", 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(all["actions"]["Blowjob"]); got != 2 {
		t.Errorf("across all runs: %d spans, want both analyses' worth", got)
	}

	for _, runID := range []int64{first, second} {
		one, err := db.GetSceneSpansByLabel(ctx, "native", 3, runID)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(one["actions"]["Blowjob"]); got != 1 {
			t.Errorf("run %d: %d spans, want exactly its own", runID, got)
		}
	}
}

func TestSceneLabelSupportsRoundTripAndLatestRun(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	input := SceneRunInput{
		Service: "llama_vlm",
		SceneID: 106502,
		LabelSupports: []StoredLabelSupport{
			{Tag: "Blowjob", StashID: "stash-1", Frames: 4, SpanCount: 2, FirstAt: 2, LastAt: 10},
		},
	}
	first, err := db.StoreSceneRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	input.LabelSupports = []StoredLabelSupport{
		{Tag: "Cowgirl", Frames: 1, SpanCount: 1, FirstAt: 12, LastAt: 12},
	}
	if _, err := db.StoreSceneRun(ctx, input); err != nil {
		t.Fatal(err)
	}

	latest, err := db.GetSceneLabelSupports(ctx, "llama_vlm", 106502, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(latest, input.LabelSupports) {
		t.Fatalf("latest supports = %#v, want %#v", latest, input.LabelSupports)
	}

	wantFirst := []StoredLabelSupport{
		{Tag: "Blowjob", StashID: "stash-1", Frames: 4, SpanCount: 2, FirstAt: 2, LastAt: 10},
	}
	gotFirst, err := db.GetSceneLabelSupports(ctx, "llama_vlm", 106502, first)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotFirst, wantFirst) {
		t.Fatalf("first supports = %#v, want %#v", gotFirst, wantFirst)
	}
}
