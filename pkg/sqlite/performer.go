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
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/utils"
	"gopkg.in/guregu/null.v4"
	"gopkg.in/guregu/null.v4/zero"
)

const (
	performerTable         = "performers"
	performerIDColumn      = "performer_id"
	performersAliasesTable = "performer_aliases"
	performerAliasColumn   = "alias"
	performersTagsTable    = "performers_tags"

	performerURLsTable = "performer_urls"
	performerURLColumn = "url"

	performerImageBlobColumn = "image_blob"

	// Ordered multi-image collection (position 0 == primary, mirrored
	// into performers.image_blob). Distinct from the existing
	// performers_images gallery-image join table.
	performerImagesTable = "performer_images"
)

type performerRow struct {
	ID                   int         `db:"id" goqu:"skipinsert"`
	Name                 null.String `db:"name"` // TODO: make schema non-nullable
	Disambigation        zero.String `db:"disambiguation"`
	Gender               zero.String `db:"gender"`
	Birthdate            NullDate    `db:"birthdate"`
	BirthdatePrecision   null.Int    `db:"birthdate_precision"`
	Ethnicity            zero.String `db:"ethnicity"`
	Country              zero.String `db:"country"`
	EyeColor             zero.String `db:"eye_color"`
	Height               null.Int    `db:"height"`
	Measurements         zero.String `db:"measurements"`
	FakeTits             zero.String `db:"fake_tits"`
	PenisLength          null.Float  `db:"penis_length"`
	Circumcised          zero.String `db:"circumcised"`
	CareerStart          NullDate    `db:"career_start"`
	CareerStartPrecision null.Int    `db:"career_start_precision"`
	CareerEnd            NullDate    `db:"career_end"`
	CareerEndPrecision   null.Int    `db:"career_end_precision"`
	Tattoos              zero.String `db:"tattoos"`
	Piercings            zero.String `db:"piercings"`
	Favorite             bool        `db:"favorite"`
	CreatedAt            Timestamp   `db:"created_at"`
	UpdatedAt            Timestamp   `db:"updated_at"`
	// expressed as 1-100
	Rating             null.Int    `db:"rating"`
	Details            zero.String `db:"details"`
	DeathDate          NullDate    `db:"death_date"`
	DeathDatePrecision null.Int    `db:"death_date_precision"`
	HairColor          zero.String `db:"hair_color"`
	Weight             null.Int    `db:"weight"`
	IgnoreAutoTag      bool        `db:"ignore_auto_tag"`

	// not used in resolution or updates
	ImageBlob zero.String `db:"image_blob"`
}

func (r *performerRow) fromPerformer(o models.Performer) {
	r.ID = o.ID
	r.Name = null.StringFrom(o.Name)
	r.Disambigation = zero.StringFrom(o.Disambiguation)
	if o.Gender != nil && o.Gender.IsValid() {
		r.Gender = zero.StringFrom(o.Gender.String())
	}
	r.Birthdate = NullDateFromDatePtr(o.Birthdate)
	r.BirthdatePrecision = datePrecisionFromDatePtr(o.Birthdate)
	r.Ethnicity = zero.StringFrom(o.Ethnicity)
	r.Country = zero.StringFrom(o.Country)
	r.EyeColor = zero.StringFrom(o.EyeColor)
	r.Height = intFromPtr(o.Height)
	r.Measurements = zero.StringFrom(o.Measurements)
	r.FakeTits = zero.StringFrom(o.FakeTits)
	r.PenisLength = null.FloatFromPtr(o.PenisLength)
	if o.Circumcised != nil && o.Circumcised.IsValid() {
		r.Circumcised = zero.StringFrom(o.Circumcised.String())
	}
	r.CareerStart = NullDateFromDatePtr(o.CareerStart)
	r.CareerStartPrecision = datePrecisionFromDatePtr(o.CareerStart)
	r.CareerEnd = NullDateFromDatePtr(o.CareerEnd)
	r.CareerEndPrecision = datePrecisionFromDatePtr(o.CareerEnd)
	r.Tattoos = zero.StringFrom(o.Tattoos)
	r.Piercings = zero.StringFrom(o.Piercings)
	r.Favorite = o.Favorite
	r.CreatedAt = Timestamp{Timestamp: o.CreatedAt}
	r.UpdatedAt = Timestamp{Timestamp: o.UpdatedAt}
	r.Rating = intFromPtr(o.Rating)
	r.Details = zero.StringFrom(o.Details)
	r.DeathDate = NullDateFromDatePtr(o.DeathDate)
	r.DeathDatePrecision = datePrecisionFromDatePtr(o.DeathDate)
	r.HairColor = zero.StringFrom(o.HairColor)
	r.Weight = intFromPtr(o.Weight)
	r.IgnoreAutoTag = o.IgnoreAutoTag
}

func (r *performerRow) resolve() *models.Performer {
	ret := &models.Performer{
		ID:             r.ID,
		Name:           r.Name.String,
		Disambiguation: r.Disambigation.String,
		Birthdate:      r.Birthdate.DatePtr(r.BirthdatePrecision),
		Ethnicity:      r.Ethnicity.String,
		Country:        r.Country.String,
		EyeColor:       r.EyeColor.String,
		Height:         nullIntPtr(r.Height),
		Measurements:   r.Measurements.String,
		FakeTits:       r.FakeTits.String,
		PenisLength:    nullFloatPtr(r.PenisLength),
		CareerStart:    r.CareerStart.DatePtr(r.CareerStartPrecision),
		CareerEnd:      r.CareerEnd.DatePtr(r.CareerEndPrecision),
		Tattoos:        r.Tattoos.String,
		Piercings:      r.Piercings.String,
		Favorite:       r.Favorite,
		CreatedAt:      r.CreatedAt.Timestamp,
		UpdatedAt:      r.UpdatedAt.Timestamp,
		// expressed as 1-100
		Rating:        nullIntPtr(r.Rating),
		Details:       r.Details.String,
		DeathDate:     r.DeathDate.DatePtr(r.DeathDatePrecision),
		HairColor:     r.HairColor.String,
		Weight:        nullIntPtr(r.Weight),
		IgnoreAutoTag: r.IgnoreAutoTag,
	}

	if r.Gender.ValueOrZero() != "" {
		v := models.GenderEnum(r.Gender.String)
		ret.Gender = &v
	}

	if r.Circumcised.ValueOrZero() != "" {
		v := models.CircumcisedEnum(r.Circumcised.String)
		ret.Circumcised = &v
	}

	return ret
}

type performerRowRecord struct {
	updateRecord
}

func (r *performerRowRecord) fromPartial(o models.PerformerPartial) {
	r.setString("name", o.Name)
	r.setNullString("disambiguation", o.Disambiguation)
	r.setNullString("gender", o.Gender)
	r.setNullDate("birthdate", "birthdate_precision", o.Birthdate)
	r.setNullString("ethnicity", o.Ethnicity)
	r.setNullString("country", o.Country)
	r.setNullString("eye_color", o.EyeColor)
	r.setNullInt("height", o.Height)
	r.setNullString("measurements", o.Measurements)
	r.setNullString("fake_tits", o.FakeTits)
	r.setNullFloat64("penis_length", o.PenisLength)
	r.setNullString("circumcised", o.Circumcised)
	r.setNullDate("career_start", "career_start_precision", o.CareerStart)
	r.setNullDate("career_end", "career_end_precision", o.CareerEnd)
	r.setNullString("tattoos", o.Tattoos)
	r.setNullString("piercings", o.Piercings)
	r.setBool("favorite", o.Favorite)
	r.setTimestamp("created_at", o.CreatedAt)
	r.setTimestamp("updated_at", o.UpdatedAt)
	r.setNullInt("rating", o.Rating)
	r.setNullString("details", o.Details)
	r.setNullDate("death_date", "death_date_precision", o.DeathDate)
	r.setNullString("hair_color", o.HairColor)
	r.setNullInt("weight", o.Weight)
	r.setBool("ignore_auto_tag", o.IgnoreAutoTag)
}

type performerRepositoryType struct {
	repository

	tags     joinRepository
	stashIDs stashIDRepository

	scenes    joinRepository
	images    joinRepository
	galleries joinRepository
}

var (
	performerRepository = performerRepositoryType{
		repository: repository{
			tableName: performerTable,
			idColumn:  idColumn,
		},
		tags: joinRepository{
			repository: repository{
				tableName: performersTagsTable,
				idColumn:  performerIDColumn,
			},
			fkColumn:     tagIDColumn,
			foreignTable: tagTable,
			orderBy:      tagTableSortSQL,
		},
		stashIDs: stashIDRepository{
			repository{
				tableName: "performer_stash_ids",
				idColumn:  performerIDColumn,
			},
		},
		scenes: joinRepository{
			repository: repository{
				tableName: performersScenesTable,
				idColumn:  performerIDColumn,
			},
			fkColumn:     sceneIDColumn,
			foreignTable: sceneTable,
		},
		images: joinRepository{
			repository: repository{
				tableName: performersImagesTable,
				idColumn:  performerIDColumn,
			},
			fkColumn:     imageIDColumn,
			foreignTable: imageTable,
		},
		galleries: joinRepository{
			repository: repository{
				tableName: performersGalleriesTable,
				idColumn:  performerIDColumn,
			},
			fkColumn:     galleryIDColumn,
			foreignTable: galleryTable,
		},
	}
)

type PerformerStore struct {
	blobJoinQueryBuilder
	customFieldsStore

	tableMgr *table
}

func NewPerformerStore(blobStore *BlobStore) *PerformerStore {
	return &PerformerStore{
		blobJoinQueryBuilder: blobJoinQueryBuilder{
			blobStore: blobStore,
			joinTable: performerTable,
		},
		customFieldsStore: customFieldsStore{
			table: performersCustomFieldsTable,
			fk:    performersCustomFieldsTable.Col(performerIDColumn),
		},
		tableMgr: performerTableMgr,
	}
}

func (qb *PerformerStore) table() exp.IdentifierExpression {
	return qb.tableMgr.table
}

func (qb *PerformerStore) selectDataset() *goqu.SelectDataset {
	return dialect.From(qb.table()).Select(qb.table().All())
}

func (qb *PerformerStore) Create(ctx context.Context, newObject *models.CreatePerformerInput) error {
	var r performerRow
	r.fromPerformer(*newObject.Performer)

	id, err := qb.tableMgr.insertID(ctx, r)
	if err != nil {
		return err
	}

	if newObject.Aliases.Loaded() {
		if err := performersAliasesTableMgr.insertJoins(ctx, id, newObject.Aliases.List()); err != nil {
			return err
		}
	}

	if newObject.URLs.Loaded() {
		const startPos = 0
		if err := performersURLsTableMgr.insertJoins(ctx, id, startPos, newObject.URLs.List()); err != nil {
			return err
		}
	}

	if newObject.TagIDs.Loaded() {
		if err := performersTagsTableMgr.insertJoins(ctx, id, newObject.TagIDs.List()); err != nil {
			return err
		}
	}

	if newObject.StashIDs.Loaded() {
		if err := performersStashIDsTableMgr.insertJoins(ctx, id, newObject.StashIDs.List()); err != nil {
			return err
		}
	}

	const partial = false
	if err := qb.setCustomFields(ctx, id, newObject.CustomFields, partial); err != nil {
		return err
	}

	updated, err := qb.find(ctx, id)
	if err != nil {
		return fmt.Errorf("finding after create: %w", err)
	}

	*newObject.Performer = *updated

	return nil
}

func (qb *PerformerStore) UpdatePartial(ctx context.Context, id int, partial models.PerformerPartial) (*models.Performer, error) {
	r := performerRowRecord{
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

	if partial.Aliases != nil {
		if err := performersAliasesTableMgr.modifyJoins(ctx, id, partial.Aliases.Values, partial.Aliases.Mode); err != nil {
			return nil, err
		}
	}

	if partial.URLs != nil {
		if err := performersURLsTableMgr.modifyJoins(ctx, id, partial.URLs.Values, partial.URLs.Mode); err != nil {
			return nil, err
		}
	}

	if partial.TagIDs != nil {
		if err := performersTagsTableMgr.modifyJoins(ctx, id, partial.TagIDs.IDs, partial.TagIDs.Mode); err != nil {
			return nil, err
		}
	}
	if partial.StashIDs != nil {
		if err := performersStashIDsTableMgr.modifyJoins(ctx, id, partial.StashIDs.StashIDs, partial.StashIDs.Mode); err != nil {
			return nil, err
		}
	}

	if err := qb.SetCustomFields(ctx, id, partial.CustomFields); err != nil {
		return nil, err
	}

	return qb.find(ctx, id)
}

func (qb *PerformerStore) Update(ctx context.Context, updatedObject *models.UpdatePerformerInput) error {
	var r performerRow
	r.fromPerformer(*updatedObject.Performer)

	if err := qb.tableMgr.updateByID(ctx, updatedObject.ID, r); err != nil {
		return err
	}

	if updatedObject.Aliases.Loaded() {
		if err := performersAliasesTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.Aliases.List()); err != nil {
			return err
		}
	}

	if updatedObject.URLs.Loaded() {
		if err := performersURLsTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.URLs.List()); err != nil {
			return err
		}
	}

	if updatedObject.TagIDs.Loaded() {
		if err := performersTagsTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.TagIDs.List()); err != nil {
			return err
		}
	}

	if updatedObject.StashIDs.Loaded() {
		if err := performersStashIDsTableMgr.replaceJoins(ctx, updatedObject.ID, updatedObject.StashIDs.List()); err != nil {
			return err
		}
	}

	if err := qb.SetCustomFields(ctx, updatedObject.ID, updatedObject.CustomFields); err != nil {
		return err
	}

	return nil
}

func (qb *PerformerStore) Destroy(ctx context.Context, id int) error {
	// must handle image checksums manually. Capture the full ordered image
	// collection before the row (and its CASCADE-linked performer_images rows)
	// are gone, so the now-orphaned blobs can be GC'd afterwards.
	checksums, err := qb.GetImageChecksums(ctx, id)
	if err != nil {
		return err
	}

	if err := qb.destroyImage(ctx, id); err != nil {
		return err
	}

	if err := performerRepository.destroyExisting(ctx, []int{id}); err != nil {
		return err
	}

	// performer_images rows are dropped by ON DELETE CASCADE without freeing
	// their blobs; GC each here (Delete no-ops for blobs still referenced
	// elsewhere, e.g. shared content-addressed images).
	for _, c := range checksums {
		if err := qb.blobStore.Delete(ctx, c); err != nil {
			return err
		}
	}

	return nil
}

// returns nil, nil if not found
func (qb *PerformerStore) Find(ctx context.Context, id int) (*models.Performer, error) {
	ret, err := qb.find(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ret, err
}

func (qb *PerformerStore) FindMany(ctx context.Context, ids []int) ([]*models.Performer, error) {
	tableMgr := performerTableMgr
	ret := make([]*models.Performer, len(ids))

	if err := batchExec(ids, defaultBatchSize, func(batch []int) error {
		q := goqu.Select("*").From(tableMgr.table).Where(tableMgr.byIDInts(batch...))
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
			return nil, fmt.Errorf("performer with id %d not found", ids[i])
		}
	}

	return ret, nil
}

// returns nil, sql.ErrNoRows if not found
func (qb *PerformerStore) find(ctx context.Context, id int) (*models.Performer, error) {
	q := qb.selectDataset().Where(qb.tableMgr.byID(id))

	ret, err := qb.get(ctx, q)
	if err != nil {
		return nil, err
	}

	return ret, nil
}

func (qb *PerformerStore) findBySubquery(ctx context.Context, sq *goqu.SelectDataset) ([]*models.Performer, error) {
	table := qb.table()

	q := qb.selectDataset().Where(
		table.Col(idColumn).Eq(
			sq,
		),
	)

	return qb.getMany(ctx, q)
}

// returns nil, sql.ErrNoRows if not found
func (qb *PerformerStore) get(ctx context.Context, q *goqu.SelectDataset) (*models.Performer, error) {
	ret, err := qb.getMany(ctx, q)
	if err != nil {
		return nil, err
	}

	if len(ret) == 0 {
		return nil, sql.ErrNoRows
	}

	return ret[0], nil
}

func (qb *PerformerStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]*models.Performer, error) {
	const single = false
	var ret []*models.Performer
	if err := queryFunc(ctx, q, single, func(r *sqlx.Rows) error {
		var f performerRow
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

func (qb *PerformerStore) FindBySceneID(ctx context.Context, sceneID int) ([]*models.Performer, error) {
	sq := dialect.From(scenesPerformersJoinTable).Select(scenesPerformersJoinTable.Col(performerIDColumn)).Where(
		scenesPerformersJoinTable.Col(sceneIDColumn).Eq(sceneID),
	)
	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting performers for scene %d: %w", sceneID, err)
	}

	return ret, nil
}

func (qb *PerformerStore) FindByImageID(ctx context.Context, imageID int) ([]*models.Performer, error) {
	sq := dialect.From(performersImagesJoinTable).Select(performersImagesJoinTable.Col(performerIDColumn)).Where(
		performersImagesJoinTable.Col(imageIDColumn).Eq(imageID),
	)
	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting performers for image %d: %w", imageID, err)
	}

	return ret, nil
}

func (qb *PerformerStore) FindByGalleryID(ctx context.Context, galleryID int) ([]*models.Performer, error) {
	sq := dialect.From(performersGalleriesJoinTable).Select(performersGalleriesJoinTable.Col(performerIDColumn)).Where(
		performersGalleriesJoinTable.Col(galleryIDColumn).Eq(galleryID),
	)
	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting performers for gallery %d: %w", galleryID, err)
	}

	return ret, nil
}

func (qb *PerformerStore) FindByNames(ctx context.Context, names []string, nocase bool) ([]*models.Performer, error) {
	clause := "name "
	if nocase {
		clause += "COLLATE NOCASE "
	}
	clause += "IN " + getInBinding(len(names))

	var args []interface{}
	for _, name := range names {
		args = append(args, name)
	}

	sq := qb.selectDataset().Prepared(true).Where(
		goqu.L(clause, args...),
	)
	ret, err := qb.getMany(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting performers by names: %w", err)
	}

	return ret, nil
}

func (qb *PerformerStore) CountByTagID(ctx context.Context, tagID int) (int, error) {
	joinTable := performersTagsJoinTable

	q := dialect.Select(goqu.COUNT("*")).From(joinTable).Where(joinTable.Col(tagIDColumn).Eq(tagID))
	return count(ctx, q)
}

func (qb *PerformerStore) Count(ctx context.Context) (int, error) {
	q := dialect.Select(goqu.COUNT("*")).From(qb.table())
	return count(ctx, q)
}

func (qb *PerformerStore) All(ctx context.Context) ([]*models.Performer, error) {
	table := qb.table()
	return qb.getMany(ctx, qb.selectDataset().Order(table.Col("name").Asc()))
}

func (qb *PerformerStore) QueryForAutoTag(ctx context.Context, words []string) ([]*models.Performer, error) {
	// TODO - Query needs to be changed to support queries of this type, and
	// this method should be removed
	table := qb.table()
	sq := dialect.From(table).Select(table.Col(idColumn))
	// TODO - disabled alias matching until we get finer control over it
	// .LeftJoin(
	// 	performersAliasesJoinTable,
	// 	goqu.On(performersAliasesJoinTable.Col(performerIDColumn).Eq(table.Col(idColumn))),
	// )

	var whereClauses []exp.Expression

	for _, w := range words {
		whereClauses = append(whereClauses, table.Col("name").Like(w+"%"))
		// TODO - see above
		// whereClauses = append(whereClauses, performersAliasesJoinTable.Col("alias").Like(w+"%"))
	}

	sq = sq.Where(
		goqu.Or(whereClauses...),
		table.Col("ignore_auto_tag").Eq(0),
	)

	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting performers for autotag: %w", err)
	}

	return ret, nil
}

func (qb *PerformerStore) makeQuery(ctx context.Context, performerFilter *models.PerformerFilterType, findFilter *models.FindFilterType) (*queryBuilder, error) {
	if performerFilter == nil {
		performerFilter = &models.PerformerFilterType{}
	}
	if findFilter == nil {
		findFilter = &models.FindFilterType{}
	}

	query := performerRepository.newQuery()
	distinctIDs(&query, performerTable)

	if q := findFilter.Q; q != nil && *q != "" {
		query.join(performersAliasesTable, "", "performer_aliases.performer_id = performers.id")
		searchColumns := []string{"performers.name", "performer_aliases.alias"}
		query.parseQueryString(searchColumns, *q)
	}

	filter := filterBuilderFromHandler(ctx, &performerFilterHandler{
		performerFilter: performerFilter,
	})

	if err := query.addFilter(filter); err != nil {
		return nil, err
	}

	var err error
	query.sortAndPagination, err = qb.getPerformerSort(findFilter)
	if err != nil {
		return nil, err
	}
	query.sortAndPagination += getPagination(findFilter)

	return &query, nil
}

func (qb *PerformerStore) Query(ctx context.Context, performerFilter *models.PerformerFilterType, findFilter *models.FindFilterType) ([]*models.Performer, int, error) {
	query, err := qb.makeQuery(ctx, performerFilter, findFilter)
	if err != nil {
		return nil, 0, err
	}

	idsResult, countResult, err := query.executeFind(ctx)
	if err != nil {
		return nil, 0, err
	}

	performers, err := qb.FindMany(ctx, idsResult)
	if err != nil {
		return nil, 0, err
	}

	return performers, countResult, nil
}

func (qb *PerformerStore) QueryCount(ctx context.Context, performerFilter *models.PerformerFilterType, findFilter *models.FindFilterType) (int, error) {
	query, err := qb.makeQuery(ctx, performerFilter, findFilter)
	if err != nil {
		return 0, err
	}

	return query.executeCount(ctx)
}

func (qb *PerformerStore) sortByOCounter(direction string) string {
	// need to sum the o_counter from scenes and images
	return " ORDER BY (" + selectPerformerOCountSQL + ") " + direction
}

func (qb *PerformerStore) sortByPlayCount(direction string) string {
	// need to sum the o_counter from scenes and images
	return " ORDER BY (" + selectPerformerPlayCountSQL + ") " + direction
}

// used for sorting on performer last o_date
var selectPerformerLastOAtSQL = utils.StrFormat(
	"SELECT MAX(o_date) FROM ("+
		"SELECT {o_date} FROM {performers_scenes} s "+
		"LEFT JOIN {scenes} ON {scenes}.id = s.{scene_id} "+
		"LEFT JOIN {scenes_o_dates} ON {scenes_o_dates}.{scene_id} = {scenes}.id "+
		"WHERE s.{performer_id} = {performers}.id"+
		")",
	map[string]interface{}{
		"performer_id":      performerIDColumn,
		"performers":        performerTable,
		"performers_scenes": performersScenesTable,
		"scenes":            sceneTable,
		"scene_id":          sceneIDColumn,
		"scenes_o_dates":    scenesODatesTable,
		"o_date":            sceneODateColumn,
	},
)

func (qb *PerformerStore) sortByLastOAt(direction string) string {
	// need to get the o_dates from scenes
	return " ORDER BY (" + selectPerformerLastOAtSQL + ") " + direction
}

// used for sorting on performer latest scene
var selectPerformerLatestSceneSQL = utils.StrFormat(
	"SELECT MAX(date) FROM ("+
		"SELECT {date} FROM {performers_scenes} s "+
		"LEFT JOIN {scenes} ON {scenes}.id = s.{scene_id} "+
		"WHERE s.{performer_id} = {performers}.id"+
		")",
	map[string]interface{}{
		"performer_id":      performerIDColumn,
		"performers":        performerTable,
		"performers_scenes": performersScenesTable,
		"scenes":            sceneTable,
		"scene_id":          sceneIDColumn,
		"date":              sceneDateColumn,
	},
)

func (qb *PerformerStore) sortByLatestScene(direction string) string {
	// need to get the latest date from scenes
	return " ORDER BY (" + selectPerformerLatestSceneSQL + ") " + direction
}

// used for sorting on performer last view_date
var selectPerformerLastPlayedAtSQL = utils.StrFormat(
	"SELECT MAX(view_date) FROM ("+
		"SELECT {view_date} FROM {performers_scenes} s "+
		"LEFT JOIN {scenes} ON {scenes}.id = s.{scene_id} "+
		"LEFT JOIN {scenes_view_dates} ON {scenes_view_dates}.{scene_id} = {scenes}.id "+
		"WHERE s.{performer_id} = {performers}.id"+
		")",
	map[string]interface{}{
		"performer_id":      performerIDColumn,
		"performers":        performerTable,
		"performers_scenes": performersScenesTable,
		"scenes":            sceneTable,
		"scene_id":          sceneIDColumn,
		"scenes_view_dates": scenesViewDatesTable,
		"view_date":         sceneViewDateColumn,
	},
)

func (qb *PerformerStore) sortByLastPlayedAt(direction string) string {
	// need to get the view_dates from scenes
	return " ORDER BY (" + selectPerformerLastPlayedAtSQL + ") " + direction
}

// used for sorting by total scene duration
var selectPerformerScenesDurationSQL = utils.StrFormat(
	"SELECT COALESCE(SUM(video_files.duration), 0) FROM {performers_scenes} s "+
		"LEFT JOIN {scenes} ON {scenes}.id = s.{scene_id} "+
		"LEFT JOIN {scenes_files} ON {scenes_files}.{scene_id} = {scenes}.id "+
		"LEFT JOIN video_files ON video_files.file_id = {scenes_files}.file_id "+
		"WHERE s.{performer_id} = {performers}.id",
	map[string]interface{}{
		"performer_id":      performerIDColumn,
		"performers":        performerTable,
		"performers_scenes": performersScenesTable,
		"scenes":            sceneTable,
		"scene_id":          sceneIDColumn,
		"scenes_files":      scenesFilesTable,
	},
)

func (qb *PerformerStore) sortByScenesDuration(direction string) string {
	// need to sum duration from all scenes for this performer
	return " ORDER BY (" + selectPerformerScenesDurationSQL + ") " + direction
}

// used for sorting by total scene file size
var selectPerformerScenesSizeSQL = utils.StrFormat(
	"SELECT COALESCE(SUM({files}.size), 0) FROM {performers_scenes} s "+
		"LEFT JOIN {scenes} ON {scenes}.id = s.{scene_id} "+
		"LEFT JOIN {scenes_files} ON {scenes_files}.{scene_id} = {scenes}.id "+
		"LEFT JOIN {files} ON {files}.id = {scenes_files}.file_id "+
		"WHERE s.{performer_id} = {performers}.id",
	map[string]interface{}{
		"performer_id":      performerIDColumn,
		"performers":        performerTable,
		"performers_scenes": performersScenesTable,
		"scenes":            sceneTable,
		"scene_id":          sceneIDColumn,
		"scenes_files":      scenesFilesTable,
		"files":             fileTable,
	},
)

func (qb *PerformerStore) sortByScenesSize(direction string) string {
	return " ORDER BY (" + selectPerformerScenesSizeSQL + ") " + direction
}

var performerSortOptions = sortOptions{
	"birthdate",
	"career_start",
	"career_end",
	"created_at",
	"galleries_count",
	"height",
	"id",
	"image_collection_count",
	"images_count",
	"last_o_at",
	"last_played_at",
	"latest_scene",
	"measurements",
	"name",
	"o_counter",
	"penis_length",
	"play_count",
	"random",
	"rating",
	"scenes_count",
	"scenes_duration",
	"scenes_size",
	"tag_count",
	"updated_at",
	"weight",
}

func (qb *PerformerStore) getPerformerSort(findFilter *models.FindFilterType) (string, error) {
	var sort string
	var direction string
	if findFilter == nil {
		sort = "name"
		direction = "ASC"
	} else {
		sort = findFilter.GetSort("name")
		direction = findFilter.GetDirection()
	}

	// CVE-2024-32231 - ensure sort is in the list of allowed sorts
	if err := performerSortOptions.validateSort(sort); err != nil {
		return "", err
	}

	sortQuery := ""
	switch sort {
	case "tag_count":
		sortQuery += getCountSort(performerTable, performersTagsTable, performerIDColumn, direction)
	case "scenes_count":
		sortQuery += getCountSort(performerTable, performersScenesTable, performerIDColumn, direction)
	case "scenes_duration":
		sortQuery += qb.sortByScenesDuration(direction)
	case "scenes_size":
		sortQuery += qb.sortByScenesSize(direction)
	case "images_count":
		sortQuery += getCountSort(performerTable, performersImagesTable, performerIDColumn, direction)
	case "image_collection_count":
		sortQuery += getCountSort(performerTable, performerImagesTable, performerIDColumn, direction)
	case "galleries_count":
		sortQuery += getCountSort(performerTable, performersGalleriesTable, performerIDColumn, direction)
	case "play_count":
		sortQuery += qb.sortByPlayCount(direction)
	case "o_counter":
		sortQuery += qb.sortByOCounter(direction)
	case "last_played_at":
		sortQuery += qb.sortByLastPlayedAt(direction)
	case "last_o_at":
		sortQuery += qb.sortByLastOAt(direction)
	case "latest_scene":
		sortQuery += qb.sortByLatestScene(direction)
	default:
		sortQuery += getSort(sort, direction, "performers")
	}

	// Whatever the sorting, always use name/id as a final sort
	sortQuery += ", COALESCE(performers.name, performers.id) COLLATE NATURAL_CI ASC"
	return sortQuery, nil
}

func (qb *PerformerStore) GetTagIDs(ctx context.Context, id int) ([]int, error) {
	return performerRepository.tags.getIDs(ctx, id)
}

func (qb *PerformerStore) GetImage(ctx context.Context, performerID int) ([]byte, error) {
	return qb.blobJoinQueryBuilder.GetImage(ctx, performerID, performerImageBlobColumn)
}

func (qb *PerformerStore) HasImage(ctx context.Context, performerID int) (bool, error) {
	return qb.blobJoinQueryBuilder.HasImage(ctx, performerID, performerImageBlobColumn)
}

// UpdateImage sets the performer's single headline image. Routed through
// the ordered collection so the legacy single-image path (scrape/identify/merge
// and external API clients) keeps performers.image_blob and performer_images in
// sync — it collapses the collection to this one image (or clears it when empty).
// The multi-image edit flow uses UpdateImages instead and is unaffected.
func (qb *PerformerStore) UpdateImage(ctx context.Context, performerID int, image []byte) error {
	if len(image) == 0 {
		return qb.SetImages(ctx, performerID, nil)
	}
	checksum, err := qb.blobStore.Write(ctx, image)
	if err != nil {
		return err
	}
	return qb.SetImages(ctx, performerID, []string{checksum})
}

func (qb *PerformerStore) destroyImage(ctx context.Context, performerID int) error {
	return qb.blobJoinQueryBuilder.DestroyImage(ctx, performerID, performerImageBlobColumn)
}

// UpdateImages writes the given raw image byte slices to the blob store (in
// order) and replaces the performer's ordered image collection with the
// resulting checksums. Empty entries are skipped; an empty result clears the
// collection. Mirrors UpdateImage for the multi-image case.
func (qb *PerformerStore) UpdateImages(ctx context.Context, performerID int, images [][]byte) error {
	checksums := make([]string, 0, len(images))
	for _, img := range images {
		if len(img) == 0 {
			continue
		}
		checksum, err := qb.blobStore.Write(ctx, img)
		if err != nil {
			return err
		}
		checksums = append(checksums, checksum)
	}
	return qb.SetImages(ctx, performerID, checksums)
}

// GetImageChecksums returns the performer's image blob checksums in display
// order (position 0 == primary). Backs the multi-image collection.
func (qb *PerformerStore) GetImageChecksums(ctx context.Context, performerID int) ([]string, error) {
	return performersImagesBlobTableMgr.get(ctx, performerID)
}

// GetImageCount returns the number of images in the performer's headshot
// collection (performer_images). Backs the image_collection_count field. The
// collection is small, so counting the ordered checksums is cheap and reuses
// the one tested read path (the sort uses a SQL COUNT — see getPerformerSort).
func (qb *PerformerStore) GetImageCount(ctx context.Context, performerID int) (int, error) {
	checksums, err := qb.GetImageChecksums(ctx, performerID)
	if err != nil {
		return 0, err
	}
	return len(checksums), nil
}

// GetImageByIndex returns the raw bytes of the performer's image at the given
// position in the ordered collection (0 == primary). Returns nil when the index
// is out of range. Backs the multi-image serve route.
func (qb *PerformerStore) GetImageByIndex(ctx context.Context, performerID int, index int) ([]byte, error) {
	checksums, err := qb.GetImageChecksums(ctx, performerID)
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(checksums) {
		return nil, nil
	}
	return qb.blobStore.Read(ctx, checksums[index])
}

// setImageChecksum points performers.image_blob at an already-stored blob (the
// position-0 mirror) without writing or deleting any blob. Pass nil to clear
// it. Blob lifecycle/GC is the caller's responsibility (SetImages/Destroy).
func (qb *PerformerStore) setImageChecksum(ctx context.Context, performerID int, checksum *string) error {
	if checksum == nil {
		q := fmt.Sprintf("UPDATE %s SET %s = NULL WHERE id = ?", performerTable, performerImageBlobColumn)
		_, err := dbWrapper.Exec(ctx, q, performerID)
		return err
	}
	q := fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", performerTable, performerImageBlobColumn)
	_, err := dbWrapper.Exec(ctx, q, *checksum, performerID)
	return err
}

// SetImages replaces the performer's ordered image collection with the given
// blob checksums (already written to the blob store by the caller). It keeps
// performers.image_blob synced to the new position-0 image and garbage-collects
// any blob that drops out of the collection.
func (qb *PerformerStore) SetImages(ctx context.Context, performerID int, checksums []string) error {
	old, err := qb.GetImageChecksums(ctx, performerID)
	if err != nil {
		return err
	}

	if err := performersImagesBlobTableMgr.replaceJoins(ctx, performerID, checksums); err != nil {
		return err
	}

	// Sync the position-0 mirror BEFORE GC so that repointing or clearing the
	// primary frees its old blob correctly — otherwise the mirror's FK would
	// block the delete and the blob would leak.
	var primary *string
	if len(checksums) > 0 {
		primary = &checksums[0]
	}
	if err := qb.setImageChecksum(ctx, performerID, primary); err != nil {
		return err
	}

	// GC any blob that left the collection. Delete no-ops while the blob is
	// still referenced (e.g. shared across performers, content-addressed).
	for _, c := range old {
		if !slices.Contains(checksums, c) {
			if err := qb.blobStore.Delete(ctx, c); err != nil {
				return err
			}
		}
	}

	return nil
}

func (qb *PerformerStore) GetAliases(ctx context.Context, performerID int) ([]string, error) {
	return performersAliasesTableMgr.get(ctx, performerID)
}

func (qb *PerformerStore) GetURLs(ctx context.Context, performerID int) ([]string, error) {
	return performersURLsTableMgr.get(ctx, performerID)
}

func (qb *PerformerStore) GetStashIDs(ctx context.Context, performerID int) ([]models.StashID, error) {
	return performersStashIDsTableMgr.get(ctx, performerID)
}

func (qb *PerformerStore) FindByStashID(ctx context.Context, stashID models.StashID) ([]*models.Performer, error) {
	sq := dialect.From(performersStashIDsJoinTable).Select(performersStashIDsJoinTable.Col(performerIDColumn)).Where(
		performersStashIDsJoinTable.Col("stash_id").Eq(stashID.StashID),
		performersStashIDsJoinTable.Col("endpoint").Eq(stashID.Endpoint),
	)
	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting performers for stash ID %s: %w", stashID.StashID, err)
	}

	return ret, nil
}

func (qb *PerformerStore) FindByStashIDStatus(ctx context.Context, hasStashID bool, stashboxEndpoint string) ([]*models.Performer, error) {
	table := qb.table()
	sq := dialect.From(table).LeftJoin(
		performersStashIDsJoinTable,
		goqu.On(table.Col(idColumn).Eq(performersStashIDsJoinTable.Col(performerIDColumn))),
	).Select(table.Col(idColumn))

	if hasStashID {
		sq = sq.Where(
			performersStashIDsJoinTable.Col("stash_id").IsNotNull(),
			performersStashIDsJoinTable.Col("endpoint").Eq(stashboxEndpoint),
		)
	} else {
		sq = sq.Where(
			performersStashIDsJoinTable.Col("stash_id").IsNull(),
		)
	}

	ret, err := qb.findBySubquery(ctx, sq)

	if err != nil {
		return nil, fmt.Errorf("getting performers for stash-box endpoint %s: %w", stashboxEndpoint, err)
	}

	return ret, nil
}

func (qb *PerformerStore) Merge(ctx context.Context, source []int, destination int) error {
	if len(source) == 0 {
		return nil
	}

	inBinding := getInBinding(len(source))

	args := []interface{}{destination}
	srcArgs := make([]interface{}, len(source))
	for i, id := range source {
		if id == destination {
			return errors.New("cannot merge where source == destination")
		}
		srcArgs[i] = id
	}

	args = append(args, srcArgs...)

	performerTables := map[string]string{
		performersScenesTable:    sceneIDColumn,
		performersGalleriesTable: galleryIDColumn,
		performersImagesTable:    imageIDColumn,
		performersTagsTable:      tagIDColumn,
	}

	args = append(args, destination)

	// for each table, update source performer ids to destination performer id, ignoring duplicates
	for table, idColumn := range performerTables {
		_, err := dbWrapper.Exec(ctx, `UPDATE OR IGNORE `+table+`
SET performer_id = ?
WHERE performer_id IN `+inBinding+`
AND NOT EXISTS(SELECT 1 FROM `+table+` o WHERE o.`+idColumn+` = `+table+`.`+idColumn+` AND o.performer_id = ?)`,
			args...,
		)
		if err != nil {
			return err
		}

		// delete source performer ids from the table where they couldn't be set
		if _, err := dbWrapper.Exec(ctx, `DELETE FROM `+table+` WHERE performer_id IN `+inBinding, srcArgs...); err != nil {
			return err
		}
	}

	for _, id := range source {
		err := qb.Destroy(ctx, id)
		if err != nil {
			return err
		}
	}

	return nil
}
