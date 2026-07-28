package aiserver

import (
	"context"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// historySink adapts the AI database to the scheduler's HistorySink, keeping
// the task package free of any storage dependency.
type historySink struct{ db *store.DB }

// RecordTask persists a terminal top-level task.
func (h historySink) RecordTask(ctx context.Context, rec task.Record, childCount int) error {
	if h.db == nil {
		return nil
	}

	entry := store.TaskHistoryEntry{
		TaskID:      rec.ID,
		ActionID:    rec.ActionID,
		Service:     rec.Service,
		Status:      string(rec.Status),
		SubmittedAt: rec.SubmittedAt,
		StartedAt:   rec.StartedAt,
		FinishedAt:  rec.FinishedAt,
	}

	if rec.StartedAt != nil && rec.FinishedAt != nil {
		ms := int64((*rec.FinishedAt - *rec.StartedAt) * 1000)
		entry.DurationMS = &ms
	}
	if childCount > 0 {
		n := int64(childCount)
		entry.ItemsSent = &n
	}
	if rec.Error != "" {
		e := rec.Error
		entry.Error = &e
	}

	// item_id identifies the single entity a detail-view task acted on.
	//
	// The Python original read task.context.isDetailView and .entityId - the
	// JSON aliases - but Pydantic exposes attributes under their FIELD names
	// (is_detail_view, entity_id). Both getattr calls therefore always missed
	// and item_id was silently null for every row ever written.
	//
	// This populates it as intended. The effect is additive: a column that was
	// always empty now carries a value, so nothing that read it can break.
	if rec.Context.IsDetailView && rec.Context.EntityID != nil && *rec.Context.EntityID != "" {
		id := *rec.Context.EntityID
		entry.ItemID = &id
	}

	return h.db.InsertTaskHistory(ctx, entry)
}
