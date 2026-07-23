package api

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/plugin/hook"
	"github.com/stashapp/stash/pkg/sliceutil"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

// used to refetch clip after hooks run
func (r *mutationResolver) getClip(ctx context.Context, id int) (ret *models.Clip, err error) {
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Clip.Find(ctx, id)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func clipFromClipCreateInput(ctx context.Context, input models.ClipCreateInput) (*models.Clip, error) {
	translator := changesetTranslator{
		inputMap: getUpdateInputMap(ctx),
	}

	newClip := models.NewClip()
	newClip.Title = translator.string(input.Title)
	newClip.Seconds = input.Seconds
	newClip.EndSeconds = input.EndSeconds
	newClip.Rating = input.Rating100

	sceneID, err := strconv.Atoi(input.SceneID)
	if err != nil {
		return nil, fmt.Errorf("converting scene id: %w", err)
	}
	newClip.SceneID = sceneID

	newClip.TagIDs, err = translator.relatedIds(input.TagIds)
	if err != nil {
		return nil, fmt.Errorf("converting tag ids: %w", err)
	}

	return &newClip, nil
}

func (r *mutationResolver) ClipCreate(ctx context.Context, input models.ClipCreateInput) (*models.Clip, error) {
	newClip, err := clipFromClipCreateInput(ctx, input)
	if err != nil {
		return nil, err
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.Clip.Create(ctx, newClip)
	}); err != nil {
		return nil, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, newClip.ID, hook.ClipCreatePost, input, nil)
	return r.getClip(ctx, newClip.ID)
}

func clipPartialFromClipUpdateInput(translator changesetTranslator, input models.ClipUpdateInput) (ret models.ClipPartial, err error) {
	updatedClip := models.NewClipPartial()

	updatedClip.Title = translator.optionalString(input.Title, "title")
	updatedClip.Seconds = translator.optionalFloat64(input.Seconds, "seconds")
	updatedClip.EndSeconds = translator.optionalFloat64(input.EndSeconds, "end_seconds")
	updatedClip.Rating = translator.optionalInt(input.Rating100, "rating100")

	updatedClip.SceneID, err = translator.optionalIntFromString(input.SceneID, "scene_id")
	if err != nil {
		return ret, fmt.Errorf("converting scene id: %w", err)
	}

	updatedClip.TagIDs, err = translator.updateIds(input.TagIds, "tag_ids")
	if err != nil {
		return ret, fmt.Errorf("converting tag ids: %w", err)
	}

	return updatedClip, nil
}

func (r *mutationResolver) ClipUpdate(ctx context.Context, input models.ClipUpdateInput) (*models.Clip, error) {
	clipID, err := strconv.Atoi(input.ID)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	translator := changesetTranslator{
		inputMap: getUpdateInputMap(ctx),
	}

	updatedClip, err := clipPartialFromClipUpdateInput(translator, input)
	if err != nil {
		return nil, err
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		_, err := r.repository.Clip.UpdatePartial(ctx, clipID, updatedClip)
		return err
	}); err != nil {
		return nil, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, clipID, hook.ClipUpdatePost, input, translator.getFields())
	return r.getClip(ctx, clipID)
}

func (r *mutationResolver) ClipDestroy(ctx context.Context, id string) (bool, error) {
	clipID, err := strconv.Atoi(id)
	if err != nil {
		return false, fmt.Errorf("converting id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.Clip.Destroy(ctx, clipID)
	}); err != nil {
		return false, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, clipID, hook.ClipDestroyPost, id, nil)

	return true, nil
}

func (r *mutationResolver) ClipsDestroy(ctx context.Context, ids []string) (bool, error) {
	clipIDs, err := stringslice.StringSliceToIntSlice(ids)
	if err != nil {
		return false, fmt.Errorf("converting ids: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		qb := r.repository.Clip
		for _, id := range clipIDs {
			if err := qb.Destroy(ctx, id); err != nil {
				return err
			}
		}

		return nil
	}); err != nil {
		return false, err
	}

	for _, id := range clipIDs {
		r.hookExecutor.ExecutePostHooks(ctx, id, hook.ClipDestroyPost, ids, nil)
	}

	return true, nil
}

func (r *mutationResolver) ClipAddO(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	clipID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	var times []time.Time

	// convert time to local time, so that sorting is consistent
	for _, tt := range t {
		times = append(times, tt.Local())
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		updatedTimes, err = r.repository.Clip.AddO(ctx, clipID, times)
		return err
	}); err != nil {
		return nil, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, clipID, hook.ClipOUpdatePost, nil, nil)
	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) ClipDeleteO(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	clipID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	var times []time.Time

	for _, tt := range t {
		times = append(times, *tt)
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		updatedTimes, err = r.repository.Clip.DeleteO(ctx, clipID, times)
		return err
	}); err != nil {
		return nil, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, clipID, hook.ClipOUpdatePost, nil, nil)
	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) ClipResetO(ctx context.Context, id string) (ret int, err error) {
	clipID, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("converting id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Clip.ResetO(ctx, clipID)
		return err
	}); err != nil {
		return 0, err
	}

	r.hookExecutor.ExecutePostHooks(ctx, clipID, hook.ClipOUpdatePost, nil, nil)
	return ret, nil
}

func (r *mutationResolver) ClipAddPlay(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	clipID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	var times []time.Time

	// convert time to local time, so that sorting is consistent
	for _, tt := range t {
		times = append(times, tt.Local())
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		updatedTimes, err = r.repository.Clip.AddViews(ctx, clipID, times)
		return err
	}); err != nil {
		return nil, err
	}

	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) ClipDeletePlay(ctx context.Context, id string, t []*time.Time) (*HistoryMutationResult, error) {
	clipID, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting id: %w", err)
	}

	var times []time.Time

	for _, tt := range t {
		times = append(times, *tt)
	}

	var updatedTimes []time.Time

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		updatedTimes, err = r.repository.Clip.DeleteViews(ctx, clipID, times)
		return err
	}); err != nil {
		return nil, err
	}

	return &HistoryMutationResult{
		Count:   len(updatedTimes),
		History: sliceutil.ValuesToPtrs(updatedTimes),
	}, nil
}

func (r *mutationResolver) ClipResetPlay(ctx context.Context, id string) (ret int, err error) {
	clipID, err := strconv.Atoi(id)
	if err != nil {
		return 0, fmt.Errorf("converting id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Clip.DeleteAllViews(ctx, clipID)
		return err
	}); err != nil {
		return 0, err
	}

	return ret, nil
}

func (r *mutationResolver) ClipGenerate(ctx context.Context, id string) (string, error) {
	jobID := manager.GetInstance().GenerateClip(ctx, id)
	return strconv.Itoa(jobID), nil
}
