package api

import (
	"context"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/tagging"
	"github.com/stashapp/stash/internal/aiserver/task"
	"github.com/stashapp/stash/internal/manager"
)

func aiTaskPriorityFromRecord(p task.Priority) AITaskPriority {
	switch p {
	case task.PriorityHigh:
		return AITaskPriorityHigh
	case task.PriorityLow:
		return AITaskPriorityLow
	default:
		return AITaskPriorityNormal
	}
}

func aiTaskStatusFromRecord(s task.Status) AITaskStatus {
	switch s {
	case task.StatusRunning:
		return AITaskStatusRunning
	case task.StatusCompleted:
		return AITaskStatusCompleted
	case task.StatusFailed:
		return AITaskStatusFailed
	case task.StatusCancelled:
		return AITaskStatusCancelled
	case task.StatusStreaming:
		return AITaskStatusStreaming
	default:
		return AITaskStatusQueued
	}
}

func toAITask(rec task.Record) *AITask {
	var errMsg *string
	if rec.Error != "" {
		errMsg = &rec.Error
	}
	var itemID *string
	if rec.Context.IsDetailView && rec.Context.EntityID != nil && *rec.Context.EntityID != "" {
		value := *rec.Context.EntityID
		itemID = &value
	}
	var parameters map[string]any
	var result any
	if rec.ActionID == tagging.ActionID {
		parameters = rec.Params
		result = rec.Result
	}
	return &AITask{
		ID:              rec.ID,
		ActionID:        rec.ActionID,
		Service:         rec.Service,
		Priority:        aiTaskPriorityFromRecord(rec.Priority),
		Status:          aiTaskStatusFromRecord(rec.Status),
		SubmittedAt:     rec.SubmittedAt,
		StartedAt:       rec.StartedAt,
		FinishedAt:      rec.FinishedAt,
		Error:           errMsg,
		ItemID:          itemID,
		Parameters:      parameters,
		Result:          result,
		CancelRequested: rec.CancelRequested,
	}
}

func (r *queryResolver) AiTasks(ctx context.Context, filter *AITaskListFilter) ([]*AITask, error) {
	mgr := manager.GetInstance()
	tasks := mgr.AIServer.Tasks()
	if tasks == nil {
		return []*AITask{}, nil
	}

	f := task.ListFilter{}
	explicitStatus := filter != nil && filter.Status != nil
	if filter != nil {
		if filter.Service != nil {
			f.Service = *filter.Service
		}
		if filter.Status != nil {
			f.Status = aiTaskStatusToRecord(*filter.Status)
		}
	}

	records := tasks.List(f)
	ret := make([]*AITask, 0, len(records))
	for _, rec := range records {
		// aiTasks documents itself as the queued/running work; terminal tasks
		// also live in aiTaskHistory (SQLite), and the scheduler keeps
		// finished records in memory for a while after completion - without
		// this filter the same task would render twice (duplicate React
		// keys) whenever both queries are combined, as the settings panel
		// does. An explicit status filter still gets what it asked for.
		if !explicitStatus && rec.Status.Terminal() {
			continue
		}
		ret = append(ret, toAITask(rec))
	}
	return ret, nil
}

func aiTaskStatusToRecord(s AITaskStatus) task.Status {
	switch s {
	case AITaskStatusRunning:
		return task.StatusRunning
	case AITaskStatusCompleted:
		return task.StatusCompleted
	case AITaskStatusFailed:
		return task.StatusFailed
	case AITaskStatusCancelled:
		return task.StatusCancelled
	case AITaskStatusStreaming:
		return task.StatusStreaming
	default:
		return task.StatusQueued
	}
}

func (r *queryResolver) AiTaskHistory(ctx context.Context, limit *int) ([]*AITask, error) {
	mgr := manager.GetInstance()
	db := mgr.AIServer.DB()
	if db == nil {
		return []*AITask{}, nil
	}

	f := store.TaskHistoryFilter{}
	if limit != nil {
		f.Limit = *limit
	}

	rows, err := db.ListTaskHistory(ctx, f)
	if err != nil {
		return nil, err
	}

	ret := make([]*AITask, len(rows))
	for i, row := range rows {
		var parameters map[string]any
		var result any
		if row.ActionID == tagging.ActionID {
			parameters = row.InputParams
			result = row.Result
		}
		ret[i] = &AITask{
			ID:              row.TaskID,
			ActionID:        row.ActionID,
			Service:         row.Service,
			Priority:        AITaskPriorityNormal,
			Status:          aiTaskStatusFromRecord(task.Status(row.Status)),
			SubmittedAt:     row.SubmittedAt,
			StartedAt:       row.StartedAt,
			FinishedAt:      row.FinishedAt,
			Error:           row.Error,
			ItemID:          row.ItemID,
			Parameters:      parameters,
			Result:          result,
			CancelRequested: false,
		}
	}
	return ret, nil
}
