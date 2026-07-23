package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/jmoiron/sqlx"
	"gopkg.in/guregu/null.v4"
	"gopkg.in/guregu/null.v4/zero"

	"github.com/stashapp/stash/pkg/models"
)

const (
	clipTable      = "clips"
	clipIDColumn   = "clip_id"
	clipsTagsTable = "clips_tags"

	clipsViewDatesTable = "clips_view_dates"
	clipViewDateColumn  = "view_date"
	clipsODatesTable    = "clips_o_dates"
	clipODateColumn     = "o_date"
)

type clipRow struct {
	ID      int         `db:"id" goqu:"skipinsert"`
	Title   zero.String `db:"title"`
	SceneID int         `db:"scene_id"`
	Seconds float64     `db:"seconds"`
	// expressed as 1-100
	Rating     null.Int   `db:"rating"`
	CreatedAt  Timestamp  `db:"created_at"`
	UpdatedAt  Timestamp  `db:"updated_at"`
	EndSeconds null.Float `db:"end_seconds"`
}

func (r *clipRow) fromClip(o models.Clip) {
	r.ID = o.ID
	r.Title = zero.StringFrom(o.Title)
	r.SceneID = o.SceneID
	r.Seconds = o.Seconds
	if o.EndSeconds != nil {
		r.EndSeconds = null.FloatFrom(*o.EndSeconds)
	}
	r.Rating = intFromPtr(o.Rating)
	r.CreatedAt = Timestamp{Timestamp: o.CreatedAt}
	r.UpdatedAt = Timestamp{Timestamp: o.UpdatedAt}
}

func (r *clipRow) resolve() *models.Clip {
	ret := &models.Clip{
		ID:         r.ID,
		Title:      r.Title.String,
		SceneID:    r.SceneID,
		Seconds:    r.Seconds,
		EndSeconds: r.EndSeconds.Ptr(),
		Rating:     nullIntPtr(r.Rating),
		CreatedAt:  r.CreatedAt.Timestamp,
		UpdatedAt:  r.UpdatedAt.Timestamp,
	}

	return ret
}

type clipRowRecord struct {
	updateRecord
}

func (r *clipRowRecord) fromPartial(o models.ClipPartial) {
	r.setNullString("title", o.Title)
	r.setInt("scene_id", o.SceneID)
	r.setFloat64("seconds", o.Seconds)
	r.setNullFloat64("end_seconds", o.EndSeconds)
	r.setNullInt("rating", o.Rating)
	r.setTimestamp("created_at", o.CreatedAt)
	r.setTimestamp("updated_at", o.UpdatedAt)
}

var clipRepository = repository{
	tableName: clipTable,
	idColumn:  idColumn,
}

type ClipStore struct {
	tagRelationshipStore
	oDateManager
	viewDateManager

	tableMgr *table
}

func NewClipStore() *ClipStore {
	return &ClipStore{
		tagRelationshipStore: tagRelationshipStore{
			idRelationshipStore: idRelationshipStore{
				joinTable: clipsTagsTableMgr,
			},
		},
		oDateManager:    oDateManager{clipsOTableMgr},
		viewDateManager: viewDateManager{clipsViewTableMgr},

		tableMgr: clipTableMgr,
	}
}

func (qb *ClipStore) table() exp.IdentifierExpression {
	return qb.tableMgr.table
}

func (qb *ClipStore) selectDataset() *goqu.SelectDataset {
	return dialect.From(qb.table()).Select(qb.table().All())
}

func (qb *ClipStore) Create(ctx context.Context, newObject *models.Clip) error {
	var r clipRow
	r.fromClip(*newObject)

	id, err := qb.tableMgr.insertID(ctx, r)
	if err != nil {
		return err
	}

	if err := qb.tagRelationshipStore.createRelationships(ctx, id, newObject.TagIDs); err != nil {
		return err
	}

	updated, err := qb.find(ctx, id)
	if err != nil {
		return fmt.Errorf("finding after create: %w", err)
	}

	*newObject = *updated

	return nil
}

func (qb *ClipStore) UpdatePartial(ctx context.Context, id int, partial models.ClipPartial) (*models.Clip, error) {
	r := clipRowRecord{
		updateRecord{
			Record: make(exp.Record),
		},
	}

	r.fromPartial(partial)

	if len(r.Record) > 0 {
		if err := qb.tableMgr.updateByID(ctx, id, r.Record); err != nil {
			return nil, err
		}
	}

	if err := qb.tagRelationshipStore.modifyRelationships(ctx, id, partial.TagIDs); err != nil {
		return nil, err
	}

	return qb.find(ctx, id)
}

func (qb *ClipStore) Update(ctx context.Context, updatedObject *models.Clip) error {
	var r clipRow
	r.fromClip(*updatedObject)

	if err := qb.tableMgr.updateByID(ctx, updatedObject.ID, r); err != nil {
		return err
	}

	if err := qb.tagRelationshipStore.replaceRelationships(ctx, updatedObject.ID, updatedObject.TagIDs); err != nil {
		return err
	}

	return nil
}

func (qb *ClipStore) Destroy(ctx context.Context, id int) error {
	return clipRepository.destroyExisting(ctx, []int{id})
}

// returns nil, nil if not found
func (qb *ClipStore) Find(ctx context.Context, id int) (*models.Clip, error) {
	ret, err := qb.find(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ret, err
}

func (qb *ClipStore) FindMany(ctx context.Context, ids []int) ([]*models.Clip, error) {
	ret := make([]*models.Clip, len(ids))

	table := qb.table()
	if err := batchExec(ids, defaultBatchSize, func(batch []int) error {
		q := qb.selectDataset().Prepared(true).Where(table.Col(idColumn).In(batch))
		unsorted, err := qb.getMany(ctx, q)
		if err != nil {
			return err
		}

		for _, s := range unsorted {
			i := slices.Index(ids, s.ID)
			ret[i] = s
		}

		return nil
	}); err != nil {
		return nil, err
	}

	for i := range ret {
		if ret[i] == nil {
			return nil, fmt.Errorf("clip with id %d not found", ids[i])
		}
	}

	return ret, nil
}

// returns nil, sql.ErrNoRows if not found
func (qb *ClipStore) find(ctx context.Context, id int) (*models.Clip, error) {
	q := qb.selectDataset().Where(qb.tableMgr.byID(id))

	ret, err := qb.get(ctx, q)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

// returns nil, sql.ErrNoRows if not found
func (qb *ClipStore) get(ctx context.Context, q *goqu.SelectDataset) (*models.Clip, error) {
	ret, err := qb.getMany(ctx, q)
	if err != nil {
		return nil, err
	}

	if len(ret) == 0 {
		return nil, sql.ErrNoRows
	}

	return ret[0], nil
}

func (qb *ClipStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]*models.Clip, error) {
	const single = false
	var ret []*models.Clip
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var f clipRow
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

func (qb *ClipStore) FindBySceneID(ctx context.Context, sceneID int) ([]*models.Clip, error) {
	query := `
		SELECT clips.* FROM clips
		WHERE clips.scene_id = ?
		GROUP BY clips.id
		ORDER BY clips.seconds ASC
	`
	args := []interface{}{sceneID}
	return qb.queryClips(ctx, query, args)
}

func (qb *ClipStore) Count(ctx context.Context) (int, error) {
	q := dialect.Select(goqu.COUNT("*")).From(qb.table())
	return count(ctx, q)
}

func (qb *ClipStore) All(ctx context.Context) ([]*models.Clip, error) {
	table := qb.table()

	return qb.getMany(ctx, qb.selectDataset().Order(
		table.Col("scene_id").Asc(),
		table.Col("seconds").Asc(),
		table.Col(idColumn).Asc(),
	))
}

func (qb *ClipStore) makeQuery(ctx context.Context, clipFilter *models.ClipFilterType, findFilter *models.FindFilterType) (*queryBuilder, error) {
	if findFilter == nil {
		findFilter = &models.FindFilterType{}
	}
	if clipFilter == nil {
		clipFilter = &models.ClipFilterType{}
	}

	query := clipRepository.newQuery()
	distinctIDs(&query, clipTable)

	if q := findFilter.Q; q != nil && *q != "" {
		searchColumns := []string{"clips.title"}
		query.parseQueryString(searchColumns, *q)
	}

	filter := filterBuilderFromHandler(ctx, &clipFilterHandler{
		clipFilter: clipFilter,
	})

	if err := query.addFilter(filter); err != nil {
		return nil, err
	}

	if err := qb.setClipSort(&query, findFilter); err != nil {
		return nil, err
	}

	query.sortAndPagination += getPagination(findFilter)

	return &query, nil
}

func (qb *ClipStore) Query(ctx context.Context, clipFilter *models.ClipFilterType, findFilter *models.FindFilterType) ([]*models.Clip, int, error) {
	query, err := qb.makeQuery(ctx, clipFilter, findFilter)
	if err != nil {
		return nil, 0, err
	}

	idsResult, countResult, err := query.executeFind(ctx)
	if err != nil {
		return nil, 0, err
	}

	clips, err := qb.FindMany(ctx, idsResult)
	if err != nil {
		return nil, 0, err
	}

	return clips, countResult, nil
}

func (qb *ClipStore) QueryCount(ctx context.Context, clipFilter *models.ClipFilterType, findFilter *models.FindFilterType) (int, error) {
	query, err := qb.makeQuery(ctx, clipFilter, findFilter)
	if err != nil {
		return 0, err
	}

	return query.executeCount(ctx)
}

var clipSortOptions = sortOptions{
	"created_at",
	"id",
	"random",
	"rating",
	"scene_id",
	"seconds",
	"title",
	"tag_count",
	"updated_at",
}

func (qb *ClipStore) setClipSort(query *queryBuilder, findFilter *models.FindFilterType) error {
	sort := findFilter.GetSort("seconds")
	direction := findFilter.GetDirection()

	// CVE-2024-32231 - ensure sort is in the list of allowed sorts
	if err := clipSortOptions.validateSort(sort); err != nil {
		return err
	}

	switch sort {
	case "tag_count":
		query.sortAndPagination += getCountSort(clipTable, clipsTagsTable, clipIDColumn, direction)
	default:
		query.sortAndPagination += getSort(sort, direction, clipTable)
	}

	// always sort by scene and start time as a final sort
	query.sortAndPagination += ", clips.scene_id ASC, clips.seconds ASC"
	return nil
}

func (qb *ClipStore) queryClips(ctx context.Context, query string, args []interface{}) ([]*models.Clip, error) {
	const single = false
	var ret []*models.Clip
	if err := clipRepository.queryFunc(ctx, query, args, single, func(r *sqlx.Rows) error {
		var f clipRow
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
