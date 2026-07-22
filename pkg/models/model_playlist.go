package models

import (
	"context"
	"time"
)

type Playlist struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	IsDefault   bool      `json:"is_default"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func NewPlaylist() Playlist {
	currentTime := time.Now()
	return Playlist{
		CreatedAt: currentTime,
		UpdatedAt: currentTime,
	}
}

type PlaylistPartial struct {
	Name        OptionalString
	Description OptionalString
	UpdatedAt   OptionalTime
}

func NewPlaylistPartial() PlaylistPartial {
	currentTime := time.Now()
	return PlaylistPartial{
		UpdatedAt: NewOptionalTime(currentTime),
	}
}

type PlaylistFilterType struct {
	Name      *StringCriterionInput `json:"name"`
	IsDefault *bool                 `json:"is_default"`
}

type FindPlaylistsResultType struct {
	Count     int         `json:"count"`
	Playlists []*Playlist `json:"playlists"`
}

type PlaylistReader interface {
	Find(ctx context.Context, id int) (*Playlist, error)
	FindByName(ctx context.Context, name string) (*Playlist, error)
	FindDefault(ctx context.Context) (*Playlist, error)
	FindMany(ctx context.Context, ids []int) ([]*Playlist, error)
	All(ctx context.Context) ([]*Playlist, error)
	Query(ctx context.Context, playlistFilter *PlaylistFilterType, findFilter *FindFilterType) ([]*Playlist, int, error)
	GetSceneIDs(ctx context.Context, playlistID int) ([]int, error)
	SceneCount(ctx context.Context, playlistID int) (int, error)
	IsSceneInPlaylist(ctx context.Context, playlistID int, sceneID int) (bool, error)
	GetPlaylistsWithScene(ctx context.Context, sceneID int) ([]*Playlist, error)
}

type PlaylistWriter interface {
	Create(ctx context.Context, newPlaylist *Playlist) error
	Update(ctx context.Context, id int, partial PlaylistPartial) (*Playlist, error)
	Destroy(ctx context.Context, id int) error
	SetDefault(ctx context.Context, id int) error
	AddScene(ctx context.Context, playlistID int, sceneID int) error
	AddScenes(ctx context.Context, playlistID int, sceneIDs []int) error
	RemoveScene(ctx context.Context, playlistID int, sceneID int) error
	ReorderScene(ctx context.Context, playlistID int, sceneID int, position int) error
	ClearScenes(ctx context.Context, playlistID int) error
}

type PlaylistReaderWriter interface {
	PlaylistReader
	PlaylistWriter
}
