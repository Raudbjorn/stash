package api

import (
	"context"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/pkg/models"
)

func (r *mutationResolver) PlaylistCreate(ctx context.Context, input PlaylistCreateInput) (*models.Playlist, error) {
	newPlaylist := models.NewPlaylist()
	newPlaylist.Name = input.Name
	if input.Description != nil {
		newPlaylist.Description = *input.Description
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.Playlist.Create(ctx, &newPlaylist)
	}); err != nil {
		return nil, err
	}

	return &newPlaylist, nil
}

func (r *mutationResolver) PlaylistUpdate(ctx context.Context, input PlaylistUpdateInput) (*models.Playlist, error) {
	id, err := strconv.Atoi(input.ID)
	if err != nil {
		return nil, fmt.Errorf("converting playlist id: %w", err)
	}

	partial := models.NewPlaylistPartial()
	if input.Name != nil {
		partial.Name = models.NewOptionalString(*input.Name)
	}
	if input.Description != nil {
		partial.Description = models.NewOptionalString(*input.Description)
	}

	var ret *models.Playlist
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Playlist.Update(ctx, id, partial)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *mutationResolver) PlaylistDestroy(ctx context.Context, id string) (bool, error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return false, fmt.Errorf("converting playlist id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.Playlist.Destroy(ctx, idInt)
	}); err != nil {
		return false, err
	}

	return true, nil
}

func (r *mutationResolver) PlaylistAddScenes(ctx context.Context, input PlaylistAddScenesInput) (bool, error) {
	playlistID, err := strconv.Atoi(input.PlaylistID)
	if err != nil {
		return false, fmt.Errorf("converting playlist id: %w", err)
	}

	sceneIDs := make([]int, len(input.SceneIds))
	for i, id := range input.SceneIds {
		sceneID, err := strconv.Atoi(id)
		if err != nil {
			return false, fmt.Errorf("converting scene id: %w", err)
		}
		sceneIDs[i] = sceneID
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		for _, sceneID := range sceneIDs {
			scene, err := r.repository.Scene.Find(ctx, sceneID)
			if err != nil {
				return err
			}
			if scene == nil {
				return fmt.Errorf("scene with id %d not found", sceneID)
			}
		}

		return r.repository.Playlist.AddScenes(ctx, playlistID, sceneIDs)
	}); err != nil {
		return false, err
	}

	return true, nil
}

func (r *mutationResolver) PlaylistRemoveScene(ctx context.Context, input PlaylistRemoveSceneInput) (bool, error) {
	playlistID, err := strconv.Atoi(input.PlaylistID)
	if err != nil {
		return false, fmt.Errorf("converting playlist id: %w", err)
	}

	sceneID, err := strconv.Atoi(input.SceneID)
	if err != nil {
		return false, fmt.Errorf("converting scene id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.Playlist.RemoveScene(ctx, playlistID, sceneID)
	}); err != nil {
		return false, err
	}

	return true, nil
}

func (r *mutationResolver) PlaylistReorderScene(ctx context.Context, input PlaylistReorderSceneInput) (bool, error) {
	playlistID, err := strconv.Atoi(input.PlaylistID)
	if err != nil {
		return false, fmt.Errorf("converting playlist id: %w", err)
	}

	sceneID, err := strconv.Atoi(input.SceneID)
	if err != nil {
		return false, fmt.Errorf("converting scene id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.Playlist.ReorderScene(ctx, playlistID, sceneID, input.Position)
	}); err != nil {
		return false, err
	}

	return true, nil
}

func (r *mutationResolver) PlaylistSetDefault(ctx context.Context, id string) (*models.Playlist, error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return nil, fmt.Errorf("converting playlist id: %w", err)
	}

	var ret *models.Playlist
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		if err := r.repository.Playlist.SetDefault(ctx, idInt); err != nil {
			return err
		}
		ret, err = r.repository.Playlist.Find(ctx, idInt)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *mutationResolver) PlaylistClearScenes(ctx context.Context, playlistID string) (bool, error) {
	id, err := strconv.Atoi(playlistID)
	if err != nil {
		return false, fmt.Errorf("converting playlist id: %w", err)
	}

	if err := r.withTxn(ctx, func(ctx context.Context) error {
		return r.repository.Playlist.ClearScenes(ctx, id)
	}); err != nil {
		return false, err
	}

	return true, nil
}

// Input types for playlist mutations
type PlaylistCreateInput struct {
	Name        string  `json:"name"`
	Description *string `json:"description"`
}

type PlaylistUpdateInput struct {
	ID          string  `json:"id"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

type PlaylistAddScenesInput struct {
	PlaylistID string   `json:"playlist_id"`
	SceneIds   []string `json:"scene_ids"`
}

type PlaylistRemoveSceneInput struct {
	PlaylistID string `json:"playlist_id"`
	SceneID    string `json:"scene_id"`
}

type PlaylistReorderSceneInput struct {
	PlaylistID string `json:"playlist_id"`
	SceneID    string `json:"scene_id"`
	Position   int    `json:"position"`
}
