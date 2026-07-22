package api

import (
	"context"
	"fmt"
	"strconv"
)

// SceneQueueAdd adds a scene to the default playlist (backwards compatibility)
func (r *mutationResolver) SceneQueueAdd(ctx context.Context, sceneID string) (bool, error) {
	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return false, fmt.Errorf("converting scene id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		scene, err := r.repository.Scene.Find(ctx, id)
		if err != nil {
			return err
		}
		if scene == nil {
			return fmt.Errorf("scene with id %d not found", id)
		}

		defaultPlaylist, err := r.repository.Playlist.FindDefault(ctx)
		if err != nil {
			return err
		}

		return r.repository.Playlist.AddScene(ctx, defaultPlaylist.ID, id)
	}); err != nil {
		return false, err
	}

	return true, nil
}

// SceneQueueRemove removes a scene from the default playlist (backwards compatibility)
func (r *mutationResolver) SceneQueueRemove(ctx context.Context, sceneID string) (bool, error) {
	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return false, fmt.Errorf("converting scene id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		defaultPlaylist, err := r.repository.Playlist.FindDefault(ctx)
		if err != nil {
			return err
		}

		return r.repository.Playlist.RemoveScene(ctx, defaultPlaylist.ID, id)
	}); err != nil {
		return false, err
	}

	return true, nil
}

// SceneQueueReorder reorders a scene in the default playlist (backwards compatibility)
func (r *mutationResolver) SceneQueueReorder(ctx context.Context, sceneID string, position int) (bool, error) {
	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return false, fmt.Errorf("converting scene id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		defaultPlaylist, err := r.repository.Playlist.FindDefault(ctx)
		if err != nil {
			return err
		}

		return r.repository.Playlist.ReorderScene(ctx, defaultPlaylist.ID, id, position)
	}); err != nil {
		return false, err
	}

	return true, nil
}

// SceneQueueClear clears the default playlist (backwards compatibility)
func (r *mutationResolver) SceneQueueClear(ctx context.Context) (bool, error) {
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		defaultPlaylist, err := r.repository.Playlist.FindDefault(ctx)
		if err != nil {
			return err
		}

		return r.repository.Playlist.ClearScenes(ctx, defaultPlaylist.ID)
	}); err != nil {
		return false, err
	}

	return true, nil
}
