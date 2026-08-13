package api

import (
	"context"

	"github.com/stashapp/stash/internal/aiserver/store"
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
	if filter != nil {
		if filter.Service != nil {
			f.Service = *filter.Service
		}
		if filter.Status != nil {
			f.Status = aiTaskStatusToRecord(*filter.Status)
		}
	}

	records := tasks.List(f)
	ret := make([]*AITask, len(records))
	for i, rec := range records {
		ret[i] = toAITask(rec)
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
			CancelRequested: false,
		}
	}
	return ret, nil
}
