package api

import (
	"context"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

// SceneQueueEntry is the struct for scene queue entries (backed by the default playlist)
type SceneQueueEntry struct {
	Scene     *models.Scene
	Position  int
	CreatedAt time.Time
}

// SceneQueue returns entries from the default playlist
func (r *queryResolver) SceneQueue(ctx context.Context) ([]*SceneQueueEntry, error) {
	var entries []*SceneQueueEntry

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		defaultPlaylist, err := r.repository.Playlist.FindDefault(ctx)
		if err != nil {
			return err
		}

		sceneIDs, err := r.repository.Playlist.GetSceneIDs(ctx, defaultPlaylist.ID)
		if err != nil {
			return err
		}

		for idx, sceneID := range sceneIDs {
			scene, err := r.repository.Scene.Find(ctx, sceneID)
			if err != nil {
				return err
			}

			if scene != nil {
				entries = append(entries, &SceneQueueEntry{
					Scene:     scene,
					Position:  idx,
					CreatedAt: time.Now(),
				})
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return entries, nil
}
