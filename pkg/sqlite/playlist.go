package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/jmoiron/sqlx"
	"gopkg.in/guregu/null.v4/zero"

	"github.com/stashapp/stash/pkg/models"
)

const (
	playlistTable        = "playlists"
	playlistEntriesTable = "playlist_entries"
)

type playlistRow struct {
	ID          int         `db:"id" goqu:"skipinsert"`
	Name        string      `db:"name"`
	Description zero.String `db:"description"`
	IsDefault   bool        `db:"is_default"`
	CreatedAt   Timestamp   `db:"created_at"`
	UpdatedAt   Timestamp   `db:"updated_at"`
}

func (r *playlistRow) fromPlaylist(o models.Playlist) {
	r.ID = o.ID
	r.Name = o.Name
	r.Description = zero.StringFrom(o.Description)
	r.IsDefault = o.IsDefault
	r.CreatedAt = Timestamp{Timestamp: o.CreatedAt}
	r.UpdatedAt = Timestamp{Timestamp: o.UpdatedAt}
}

func (r *playlistRow) resolve() *models.Playlist {
	return &models.Playlist{
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description.String,
		IsDefault:   r.IsDefault,
		CreatedAt:   r.CreatedAt.Timestamp,
		UpdatedAt:   r.UpdatedAt.Timestamp,
	}
}

type playlistRowRecord struct {
	updateRecord
}

func (r *playlistRowRecord) fromPartial(o models.PlaylistPartial) {
	r.setString("name", o.Name)
	r.setNullString("description", o.Description)
	r.setTimestamp("updated_at", o.UpdatedAt)
}

type PlaylistStore struct {
	repository
	tableMgr *table
}

func NewPlaylistStore() *PlaylistStore {
	return &PlaylistStore{
		repository: repository{
			tableName: playlistTable,
			idColumn:  idColumn,
		},
		tableMgr: playlistTableMgr,
	}
}

func (qb *PlaylistStore) table() exp.IdentifierExpression {
	return qb.tableMgr.table
}

func (qb *PlaylistStore) entriesTable() exp.IdentifierExpression {
	return goqu.T(playlistEntriesTable)
}

func (qb *PlaylistStore) selectDataset() *goqu.SelectDataset {
	return dialect.From(qb.table()).Select(qb.table().All())
}

func (qb *PlaylistStore) Create(ctx context.Context, newObject *models.Playlist) error {
	var r playlistRow
	r.fromPlaylist(*newObject)

	id, err := qb.tableMgr.insertID(ctx, r)
	if err != nil {
		return fmt.Errorf("inserting playlist: %w", err)
	}

	updated, err := qb.find(ctx, id)
	if err != nil {
		return fmt.Errorf("finding after create: %w", err)
	}

	*newObject = *updated
	return nil
}

func (qb *PlaylistStore) Update(ctx context.Context, id int, partial models.PlaylistPartial) (*models.Playlist, error) {
	r := playlistRowRecord{
		updateRecord{
			Record: make(exp.Record),
		},
	}

	r.fromPartial(partial)

	if len(r.Record) > 0 {
		if err := qb.tableMgr.updateByID(ctx, id, r.Record); err != nil {
			return nil, fmt.Errorf("updating playlist: %w", err)
		}
	}

	return qb.find(ctx, id)
}

func (qb *PlaylistStore) SetDefault(ctx context.Context, id int) error {
	// Clear is_default on all playlists
	clearQ := dialect.Update(qb.table()).Set(goqu.Record{"is_default": false})
	if _, err := exec(ctx, clearQ); err != nil {
		return fmt.Errorf("clearing default playlists: %w", err)
	}

	// Set is_default on the target playlist
	setQ := dialect.Update(qb.table()).Set(goqu.Record{"is_default": true}).Where(qb.tableMgr.byID(id))
	if _, err := exec(ctx, setQ); err != nil {
		return fmt.Errorf("setting default playlist: %w", err)
	}

	return nil
}

func (qb *PlaylistStore) Destroy(ctx context.Context, id int) error {
	playlist, err := qb.find(ctx, id)
	if err != nil {
		return err
	}
	if playlist.IsDefault {
		return errors.New("cannot delete the default playlist")
	}

	return qb.destroyExisting(ctx, []int{id})
}

// returns nil, nil if not found
func (qb *PlaylistStore) Find(ctx context.Context, id int) (*models.Playlist, error) {
	ret, err := qb.find(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ret, err
}

// returns nil, sql.ErrNoRows if not found
func (qb *PlaylistStore) find(ctx context.Context, id int) (*models.Playlist, error) {
	q := qb.selectDataset().Where(qb.tableMgr.byID(id))

	ret, err := qb.get(ctx, q)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (qb *PlaylistStore) get(ctx context.Context, q *goqu.SelectDataset) (*models.Playlist, error) {
	ret, err := qb.getMany(ctx, q)
	if err != nil {
		return nil, err
	}

	if len(ret) == 0 {
		return nil, sql.ErrNoRows
	}

	return ret[0], nil
}

func (qb *PlaylistStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]*models.Playlist, error) {
	const single = false
	var ret []*models.Playlist
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var f playlistRow
		if err := r.StructScan(&f); err != nil {
			return err
		}

		s := f.resolve()
		ret = append(ret, s)
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (qb *PlaylistStore) FindByName(ctx context.Context, name string) (*models.Playlist, error) {
	q := qb.selectDataset().Where(qb.table().Col("name").Eq(name))

	ret, err := qb.get(ctx, q)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ret, err
}

func (qb *PlaylistStore) FindDefault(ctx context.Context) (*models.Playlist, error) {
	q := qb.selectDataset().Where(qb.table().Col("is_default").Eq(true))

	ret, err := qb.get(ctx, q)
	if errors.Is(err, sql.ErrNoRows) {
		// Create default playlist if it doesn't exist
		newPlaylist := models.NewPlaylist()
		newPlaylist.Name = "Default Playlist"
		newPlaylist.IsDefault = true
		if err := qb.Create(ctx, &newPlaylist); err != nil {
			return nil, fmt.Errorf("creating default playlist: %w", err)
		}
		return &newPlaylist, nil
	}
	return ret, err
}

func (qb *PlaylistStore) FindMany(ctx context.Context, ids []int) ([]*models.Playlist, error) {
	q := qb.selectDataset().Where(qb.table().Col(idColumn).In(ids))
	return qb.getMany(ctx, q)
}

func (qb *PlaylistStore) All(ctx context.Context) ([]*models.Playlist, error) {
	q := qb.selectDataset().Order(qb.table().Col("name").Asc())
	return qb.getMany(ctx, q)
}

func (qb *PlaylistStore) Query(ctx context.Context, playlistFilter *models.PlaylistFilterType, findFilter *models.FindFilterType) ([]*models.Playlist, int, error) {
	if findFilter == nil {
		findFilter = &models.FindFilterType{}
	}

	q := qb.selectDataset()
	countQ := dialect.From(qb.table()).Select(goqu.COUNT("*"))

	if playlistFilter != nil {
		if playlistFilter.Name != nil {
			like := "%" + playlistFilter.Name.Value + "%"
			q = q.Where(qb.table().Col("name").Like(like))
			countQ = countQ.Where(qb.table().Col("name").Like(like))
		}
		if playlistFilter.IsDefault != nil {
			q = q.Where(qb.table().Col("is_default").Eq(*playlistFilter.IsDefault))
			countQ = countQ.Where(qb.table().Col("is_default").Eq(*playlistFilter.IsDefault))
		}
	}

	var count int
	if err := querySimple(ctx, countQ, &count); err != nil {
		return nil, 0, fmt.Errorf("counting playlists: %w", err)
	}

	sortBy := "name"
	sortDir := "ASC"
	if findFilter.Sort != nil && *findFilter.Sort != "" {
		sortBy = *findFilter.Sort
	}
	if findFilter.Direction != nil {
		sortDir = string(*findFilter.Direction)
	}

	if sortDir == "DESC" {
		q = q.Order(goqu.I(sortBy).Desc())
	} else {
		q = q.Order(goqu.I(sortBy).Asc())
	}

	if findFilter.PerPage != nil && *findFilter.PerPage > 0 {
		q = q.Limit(uint(*findFilter.PerPage))
		if findFilter.Page != nil && *findFilter.Page > 1 {
			q = q.Offset(uint((*findFilter.Page - 1) * *findFilter.PerPage))
		}
	}

	playlists, err := qb.getMany(ctx, q)
	if err != nil {
		return nil, 0, err
	}

	return playlists, count, nil
}

func (qb *PlaylistStore) SceneCount(ctx context.Context, playlistID int) (int, error) {
	q := dialect.From(qb.entriesTable()).
		Select(goqu.COUNT("*")).
		Where(qb.entriesTable().Col("playlist_id").Eq(playlistID))

	var count int
	if err := querySimple(ctx, q, &count); err != nil {
		return 0, fmt.Errorf("counting playlist scenes: %w", err)
	}

	return count, nil
}

func (qb *PlaylistStore) GetSceneIDs(ctx context.Context, playlistID int) ([]int, error) {
	q := dialect.From(qb.entriesTable()).
		Select(qb.entriesTable().Col("scene_id")).
		Where(qb.entriesTable().Col("playlist_id").Eq(playlistID)).
		Order(qb.entriesTable().Col("position").Asc())

	const single = false
	var ret []int
	if err := queryFunc(ctx, q, single, func(rows *sqlx.Rows) error {
		var sceneID int
		if err := rows.Scan(&sceneID); err != nil {
			return err
		}
		ret = append(ret, sceneID)
		return nil
	}); err != nil {
		return nil, err
	}

	return ret, nil
}

func (qb *PlaylistStore) IsSceneInPlaylist(ctx context.Context, playlistID int, sceneID int) (bool, error) {
	q := dialect.From(qb.entriesTable()).
		Select(goqu.COUNT("*")).
		Where(
			qb.entriesTable().Col("playlist_id").Eq(playlistID),
			qb.entriesTable().Col("scene_id").Eq(sceneID),
		)

	var count int
	if err := querySimple(ctx, q, &count); err != nil {
		return false, err
	}

	return count > 0, nil
}

func (qb *PlaylistStore) GetPlaylistsWithScene(ctx context.Context, sceneID int) ([]*models.Playlist, error) {
	q := qb.selectDataset().
		InnerJoin(qb.entriesTable(), goqu.On(qb.table().Col("id").Eq(qb.entriesTable().Col("playlist_id")))).
		Where(qb.entriesTable().Col("scene_id").Eq(sceneID)).
		Order(qb.table().Col("name").Asc())

	return qb.getMany(ctx, q)
}

func (qb *PlaylistStore) AddScene(ctx context.Context, playlistID int, sceneID int) error {
	maxPosQuery := dialect.From(qb.entriesTable()).
		Select(goqu.MAX(qb.entriesTable().Col("position"))).
		Where(qb.entriesTable().Col("playlist_id").Eq(playlistID))

	var maxPos sql.NullInt64
	if err := querySimple(ctx, maxPosQuery, &maxPos); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("getting max position: %w", err)
	}

	newPos := 0
	if maxPos.Valid {
		newPos = int(maxPos.Int64) + 1
	}

	q := dialect.Insert(qb.entriesTable()).Rows(
		goqu.Record{
			"playlist_id": playlistID,
			"scene_id":    sceneID,
			"position":    newPos,
		},
	).OnConflict(goqu.DoNothing())

	if _, err := exec(ctx, q); err != nil {
		return fmt.Errorf("adding scene to playlist: %w", err)
	}

	return nil
}

func (qb *PlaylistStore) AddScenes(ctx context.Context, playlistID int, sceneIDs []int) error {
	for _, sceneID := range sceneIDs {
		if err := qb.AddScene(ctx, playlistID, sceneID); err != nil {
			return err
		}
	}
	return nil
}

func (qb *PlaylistStore) RemoveScene(ctx context.Context, playlistID int, sceneID int) error {
	var removedPos int
	posQuery := dialect.From(qb.entriesTable()).
		Select(qb.entriesTable().Col("position")).
		Where(
			qb.entriesTable().Col("playlist_id").Eq(playlistID),
			qb.entriesTable().Col("scene_id").Eq(sceneID),
		)

	if err := querySimple(ctx, posQuery, &removedPos); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // Scene not in playlist, nothing to do
		}
		return fmt.Errorf("getting scene position: %w", err)
	}

	deleteQ := dialect.Delete(qb.entriesTable()).Where(
		qb.entriesTable().Col("playlist_id").Eq(playlistID),
		qb.entriesTable().Col("scene_id").Eq(sceneID),
	)
	if _, err := exec(ctx, deleteQ); err != nil {
		return fmt.Errorf("removing scene from playlist: %w", err)
	}

	updateQ := dialect.Update(qb.entriesTable()).
		Set(goqu.Record{"position": goqu.L("position - 1")}).
		Where(
			qb.entriesTable().Col("playlist_id").Eq(playlistID),
			qb.entriesTable().Col("position").Gt(removedPos),
		)

	if _, err := exec(ctx, updateQ); err != nil {
		return fmt.Errorf("updating playlist positions: %w", err)
	}

	return nil
}

func (qb *PlaylistStore) ReorderScene(ctx context.Context, playlistID int, sceneID int, newPosition int) error {
	var currentPos int
	posQuery := dialect.From(qb.entriesTable()).
		Select(qb.entriesTable().Col("position")).
		Where(
			qb.entriesTable().Col("playlist_id").Eq(playlistID),
			qb.entriesTable().Col("scene_id").Eq(sceneID),
		)

	if err := querySimple(ctx, posQuery, &currentPos); err != nil {
		return fmt.Errorf("getting current position: %w", err)
	}

	if currentPos == newPosition {
		return nil // No change needed
	}

	if newPosition < currentPos {
		updateQ := dialect.Update(qb.entriesTable()).
			Set(goqu.Record{"position": goqu.L("position + 1")}).
			Where(
				qb.entriesTable().Col("playlist_id").Eq(playlistID),
				qb.entriesTable().Col("position").Gte(newPosition),
				qb.entriesTable().Col("position").Lt(currentPos),
			)
		if _, err := exec(ctx, updateQ); err != nil {
			return fmt.Errorf("updating playlist positions: %w", err)
		}
	} else {
		updateQ := dialect.Update(qb.entriesTable()).
			Set(goqu.Record{"position": goqu.L("position - 1")}).
			Where(
				qb.entriesTable().Col("playlist_id").Eq(playlistID),
				qb.entriesTable().Col("position").Gt(currentPos),
				qb.entriesTable().Col("position").Lte(newPosition),
			)
		if _, err := exec(ctx, updateQ); err != nil {
			return fmt.Errorf("updating playlist positions: %w", err)
		}
	}

	updateSceneQ := dialect.Update(qb.entriesTable()).
		Set(goqu.Record{"position": newPosition}).
		Where(
			qb.entriesTable().Col("playlist_id").Eq(playlistID),
			qb.entriesTable().Col("scene_id").Eq(sceneID),
		)

	if _, err := exec(ctx, updateSceneQ); err != nil {
		return fmt.Errorf("updating scene position: %w", err)
	}

	return nil
}

func (qb *PlaylistStore) ClearScenes(ctx context.Context, playlistID int) error {
	q := dialect.Delete(qb.entriesTable()).Where(
		qb.entriesTable().Col("playlist_id").Eq(playlistID),
	)

	if _, err := exec(ctx, q); err != nil {
		return fmt.Errorf("clearing playlist: %w", err)
	}

	return nil
}
