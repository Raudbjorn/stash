package store

import (
	"context"
	"fmt"
	"testing"
)

func entry(id string) TaskHistoryEntry {
	started, finished := 1000.0, 1002.5
	dur := int64(2500)
	return TaskHistoryEntry{
		TaskID:      id,
		ActionID:    "skier_aitagging.tag_scenes",
		Service:     "AI_Tagging",
		Status:      "completed",
		SubmittedAt: 999.5,
		StartedAt:   &started,
		FinishedAt:  &finished,
		DurationMS:  &dur,
	}
}

func TestTaskHistoryRoundTrip(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	e := entry("task-1")
	itemID := "scene-42"
	items := int64(3)
	errText := "boom"
	e.ItemID = &itemID
	e.ItemsSent = &items
	e.Error = &errText
	e.InputParams = map[string]any{"analysis_service": "voyage"}
	e.Result = map[string]any{"spans": 14, "message": "done"}

	if err := db.InsertTaskHistory(ctx, e); err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, err := db.ListTaskHistory(ctx, TaskHistoryFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d rows, want 1", len(got))
	}

	g := got[0]
	if g.TaskID != "task-1" || g.ActionID != "skier_aitagging.tag_scenes" {
		t.Errorf("identifiers wrong: %+v", g)
	}
	// Epoch seconds as REAL: the frontend reads and sorts on these as numbers.
	if g.SubmittedAt != 999.5 || g.StartedAt == nil || *g.StartedAt != 1000.0 {
		t.Errorf("timestamps wrong: %+v", g)
	}
	if g.ItemID == nil || *g.ItemID != "scene-42" {
		t.Errorf("item_id = %v, want scene-42", g.ItemID)
	}
	if g.ItemsSent == nil || *g.ItemsSent != 3 {
		t.Errorf("items_sent = %v", g.ItemsSent)
	}
	if g.Error == nil || *g.Error != "boom" {
		t.Errorf("error = %v", g.Error)
	}
	if g.InputParams["analysis_service"] != "voyage" {
		t.Errorf("input_params = %#v", g.InputParams)
	}
	result, ok := g.Result.(map[string]any)
	if !ok || result["spans"] != float64(14) || result["message"] != "done" {
		t.Errorf("result = %#v", g.Result)
	}
}

// Re-recording the same task must not duplicate it.
func TestTaskHistoryInsertIsIdempotent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if err := db.InsertTaskHistory(ctx, entry("same-id")); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	n, err := db.CountTaskHistory(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("stored %d rows, want 1", n)
	}
}

// Once the table passes the high-water mark it is trimmed back to the low one,
// so pruning cost is amortised rather than paid on every insert.
func TestTaskHistoryPruning(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	for i := 0; i < historyHighWater; i++ {
		if err := db.InsertTaskHistory(ctx, entry(fmt.Sprintf("task-%04d", i))); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	n, err := db.CountTaskHistory(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != historyHighWater {
		t.Fatalf("at the threshold there are %d rows, want %d", n, historyHighWater)
	}

	// One more crosses the line and triggers the trim.
	if err := db.InsertTaskHistory(ctx, entry("task-overflow")); err != nil {
		t.Fatalf("insert overflow: %v", err)
	}

	n, err = db.CountTaskHistory(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != historyLowWater {
		t.Errorf("after pruning there are %d rows, want %d", n, historyLowWater)
	}

	// The newest row must survive; the oldest must not.
	rows, err := db.ListTaskHistory(ctx, TaskHistoryFilter{Limit: 500})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range rows {
		ids[r.TaskID] = true
	}
	if !ids["task-overflow"] {
		t.Error("pruning removed the newest row")
	}
	if ids["task-0000"] {
		t.Error("pruning kept the oldest row")
	}
}

func TestTaskHistoryFilters(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	mk := func(id, service, status string) TaskHistoryEntry {
		e := entry(id)
		e.Service = service
		e.Status = status
		return e
	}
	for _, e := range []TaskHistoryEntry{
		mk("a", "AI_Tagging", "completed"),
		mk("b", "AI_Tagging", "failed"),
		mk("c", "Other", "completed"),
	} {
		if err := db.InsertTaskHistory(ctx, e); err != nil {
			t.Fatalf("insert %s: %v", e.TaskID, err)
		}
	}

	if got, _ := db.ListTaskHistory(ctx, TaskHistoryFilter{Service: "AI_Tagging"}); len(got) != 2 {
		t.Errorf("service filter returned %d, want 2", len(got))
	}
	if got, _ := db.ListTaskHistory(ctx, TaskHistoryFilter{Status: "completed"}); len(got) != 2 {
		t.Errorf("status filter returned %d, want 2", len(got))
	}
	if got, _ := db.ListTaskHistory(ctx, TaskHistoryFilter{Service: "AI_Tagging", Status: "failed"}); len(got) != 1 {
		t.Errorf("combined filter returned %d, want 1", len(got))
	}

	// The limit is clamped to 500, matching the API.
	if got, _ := db.ListTaskHistory(ctx, TaskHistoryFilter{Limit: 100000}); len(got) != 3 {
		t.Errorf("oversized limit returned %d rows", len(got))
	}

	// Empty rather than nil, so the endpoint emits [].
	empty, err := db.ListTaskHistory(ctx, TaskHistoryFilter{Service: "nothing"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if empty == nil {
		t.Error("ListTaskHistory returned nil; the endpoint must emit []")
	}
}

// Newest first is what the dashboard renders.
func TestTaskHistoryOrdering(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := db.InsertTaskHistory(ctx, entry(fmt.Sprintf("task-%d", i))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	got, err := db.ListTaskHistory(ctx, TaskHistoryFilter{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("listed %d", len(got))
	}
	if got[0].TaskID != "task-4" {
		t.Errorf("first row = %s, want the newest (task-4)", got[0].TaskID)
	}
}
