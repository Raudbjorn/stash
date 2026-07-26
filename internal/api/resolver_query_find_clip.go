package api

import (
	"context"
	"strconv"

	"github.com/stashapp/stash/pkg/models"
)

func (r *queryResolver) FindClip(ctx context.Context, id string) (ret *models.Clip, err error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return nil, err
	}

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Clip.Find(ctx, idInt)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *queryResolver) FindClips(ctx context.Context, clipFilter *models.ClipFilterType, filter *models.FindFilterType) (ret *FindClipsResultType, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		clips, total, err := r.repository.Clip.Query(ctx, clipFilter, filter)
		if err != nil {
			return err
		}

		ret = &FindClipsResultType{
			Count: total,
			Clips: clips,
		}
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}
