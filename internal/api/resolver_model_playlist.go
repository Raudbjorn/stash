package api

import (
	"context"

	"github.com/stashapp/stash/internal/api/urlbuilders"
	"github.com/stashapp/stash/pkg/models"
)

func (r *playlistResolver) SceneCount(ctx context.Context, obj *models.Playlist) (int, error) {
	var count int
	var err error

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		count, err = r.repository.Playlist.SceneCount(ctx, obj.ID)
		return err
	}); err != nil {
		return 0, err
	}

	return count, nil
}

func (r *playlistResolver) FrontImagePath(ctx context.Context, obj *models.Playlist) (*string, error) {
	baseURL, _ := ctx.Value(BaseURLCtxKey).(string)
	var imagePath *string

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		sceneIDs, err := r.repository.Playlist.GetSceneIDs(ctx, obj.ID)
		if err != nil {
			return err
		}

		if len(sceneIDs) > 0 {
			scene, err := r.repository.Scene.Find(ctx, sceneIDs[0])
			if err != nil {
				return err
			}

			if scene != nil {
				builder := urlbuilders.NewSceneURLBuilder(baseURL, scene)
				screenshotPath := builder.GetScreenshotURL()
				imagePath = &screenshotPath
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return imagePath, nil
}
