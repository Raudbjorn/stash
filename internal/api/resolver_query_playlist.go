package api

import (
	"context"
	"strconv"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

func (r *queryResolver) FindPlaylist(ctx context.Context, id string) (ret *models.Playlist, err error) {
	idInt, err := strconv.Atoi(id)
	if err != nil {
		return nil, err
	}

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Playlist.Find(ctx, idInt)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *queryResolver) FindPlaylists(ctx context.Context, filter *models.FindFilterType, playlistFilter *models.PlaylistFilterType) (ret *models.FindPlaylistsResultType, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		playlists, count, err := r.repository.Playlist.Query(ctx, playlistFilter, filter)
		if err != nil {
			return err
		}

		ret = &models.FindPlaylistsResultType{
			Count:     count,
			Playlists: playlists,
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *queryResolver) AllPlaylists(ctx context.Context) (ret []*models.Playlist, err error) {
	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Playlist.All(ctx)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *queryResolver) DefaultPlaylist(ctx context.Context) (ret *models.Playlist, err error) {
	if err := r.withTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Playlist.FindDefault(ctx)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (r *queryResolver) PlaylistEntries(ctx context.Context, playlistID string) ([]*PlaylistEntry, error) {
	id, err := strconv.Atoi(playlistID)
	if err != nil {
		return nil, err
	}

	var entries []*PlaylistEntry

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		sceneIDs, err := r.repository.Playlist.GetSceneIDs(ctx, id)
		if err != nil {
			return err
		}

		for idx, sceneID := range sceneIDs {
			scene, err := r.repository.Scene.Find(ctx, sceneID)
			if err != nil {
				return err
			}

			if scene != nil {
				entries = append(entries, &PlaylistEntry{
					Scene:    scene,
					Position: idx,
				})
			}
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return entries, nil
}

func (r *queryResolver) PlaylistsWithScene(ctx context.Context, sceneID string) (ret []*models.Playlist, err error) {
	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return nil, err
	}

	if err := r.withReadTxn(ctx, func(ctx context.Context) error {
		ret, err = r.repository.Playlist.GetPlaylistsWithScene(ctx, id)
		return err
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

// PlaylistEntry is the struct for playlist entries returned by the API
type PlaylistEntry struct {
	Scene     *models.Scene
	Position  int
	CreatedAt time.Time
}
