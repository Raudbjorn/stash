package api

import (
	"context"
	"time"

	"github.com/stashapp/stash/internal/api/loaders"
	"github.com/stashapp/stash/internal/api/urlbuilders"
	"github.com/stashapp/stash/pkg/models"
)

func (r *clipResolver) Scene(ctx context.Context, obj *models.Clip) (ret *models.Scene, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Scene.Find(ctx, obj.SceneID)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *clipResolver) Rating100(ctx context.Context, obj *models.Clip) (*int, error) {
	return obj.Rating, nil
}

func (r *clipResolver) Tags(ctx context.Context, obj *models.Clip) (ret []*models.Tag, err error) {
	if !obj.TagIDs.Loaded() {
		if err := r.withReadTxn(ctx, func(ctx context.Context) error {
			return obj.LoadTagIDs(ctx, r.repository.Clip)
		}); err != nil {
			return nil, err
		}
	}

	var errs []error
	ret, errs = loaders.From(ctx).TagByID.LoadAll(obj.TagIDs.List())
	return ret, firstError(errs)
}

func (r *clipResolver) OCounter(ctx context.Context, obj *models.Clip) (*int, error) {
	ret, err := loaders.From(ctx).ClipOCount.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

func (r *clipResolver) LastPlayedAt(ctx context.Context, obj *models.Clip) (*time.Time, error) {
	ret, err := loaders.From(ctx).ClipLastPlayed.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *clipResolver) PlayCount(ctx context.Context, obj *models.Clip) (*int, error) {
	ret, err := loaders.From(ctx).ClipPlayCount.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	return &ret, nil
}

func (r *clipResolver) PlayHistory(ctx context.Context, obj *models.Clip) ([]*time.Time, error) {
	ret, err := loaders.From(ctx).ClipPlayHistory.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	// convert to pointer slice
	ptrRet := make([]*time.Time, len(ret))
	for i, t := range ret {
		tt := t
		ptrRet[i] = &tt
	}

	return ptrRet, nil
}

func (r *clipResolver) Stream(ctx context.Context, obj *models.Clip) (string, error) {
	baseURL, _ := ctx.Value(BaseURLCtxKey).(string)
	return urlbuilders.NewClipURLBuilder(baseURL, obj).GetStreamURL(), nil
}

func (r *clipResolver) Preview(ctx context.Context, obj *models.Clip) (string, error) {
	baseURL, _ := ctx.Value(BaseURLCtxKey).(string)
	return urlbuilders.NewClipURLBuilder(baseURL, obj).GetPreviewURL(), nil
}

func (r *clipResolver) OHistory(ctx context.Context, obj *models.Clip) ([]*time.Time, error) {
	ret, err := loaders.From(ctx).ClipOHistory.Load(obj.ID)
	if err != nil {
		return nil, err
	}

	// convert to pointer slice
	ptrRet := make([]*time.Time, len(ret))
	for i, t := range ret {
		tt := t
		ptrRet[i] = &tt
	}

	return ptrRet, nil
}
