package sqlite

import (
	"context"

	"github.com/doug-martin/goqu/v9"
	"github.com/doug-martin/goqu/v9/exp"
	"github.com/jmoiron/sqlx"
	"gopkg.in/guregu/null.v4"

	"github.com/stashapp/stash/pkg/models"
)

const funscriptIndexTable = "funscript_index"

// funscriptIndexRow is the db scan/insert representation of a
// models.FunscriptIndex (see migration 95 for the schema).
type funscriptIndexRow struct {
	ID       int         `db:"id" goqu:"skipinsert"`
	Filename string      `db:"filename"`
	Metadata null.String `db:"metadata"`
	SceneID  null.Int    `db:"scene_id"`
	MD5      string      `db:"md5"`
}

func (r *funscriptIndexRow) fromModel(o models.FunscriptIndex) {
	r.ID = o.ID
	r.Filename = o.Filename
	if o.Metadata != "" {
		r.Metadata = null.StringFrom(o.Metadata)
	}
	if o.SceneID != nil {
		r.SceneID = null.IntFrom(int64(*o.SceneID))
	}
	r.MD5 = o.MD5
}

func (r *funscriptIndexRow) resolve() models.FunscriptIndex {
	ret := models.FunscriptIndex{
		ID:       r.ID,
		Filename: r.Filename,
		Metadata: r.Metadata.String,
		MD5:      r.MD5,
	}
	if r.SceneID.Valid {
		v := int(r.SceneID.Int64)
		ret.SceneID = &v
	}
	return ret
}

// FunscriptIndexStore persists the local funscript index. It follows the
// goqu/dbWrapper/context-txn pattern of the other compact stores: the store
// holds no connection; every method reads the active transaction from ctx (via
// the package-level dbWrapper used by the shared query helpers).
type FunscriptIndexStore struct {
	tableMgr *table
}

// NewFunscriptIndexStore constructs a FunscriptIndexStore.
func NewFunscriptIndexStore() *FunscriptIndexStore {
	return &FunscriptIndexStore{
		tableMgr: funscriptIndexTableMgr,
	}
}

func (qb *FunscriptIndexStore) table() exp.IdentifierExpression {
	return qb.tableMgr.table
}

func (qb *FunscriptIndexStore) selectDataset() *goqu.SelectDataset {
	return dialect.From(qb.table()).Select(qb.table().All())
}

// Create implements models.FunscriptIndexWriter.
func (qb *FunscriptIndexStore) Create(ctx context.Context, f *models.FunscriptIndex) error {
	var r funscriptIndexRow
	r.fromModel(*f)

	id, err := qb.tableMgr.insertID(ctx, r)
	if err != nil {
		return err
	}
	f.ID = id
	return nil
}

// SetSceneID implements models.FunscriptIndexWriter.
func (qb *FunscriptIndexStore) SetSceneID(ctx context.Context, id int, sceneID int) error {
	return qb.tableMgr.updateByID(ctx, id, goqu.Record{"scene_id": sceneID})
}

// ExistsByFilename implements models.FunscriptIndexReader.
func (qb *FunscriptIndexStore) ExistsByFilename(ctx context.Context, filename string) (bool, error) {
	q := dialect.From(qb.table()).
		Prepared(true).
		Select(goqu.COUNT("*")).
		Where(qb.table().Col("filename").Eq(filename))

	c, err := count(ctx, q)
	if err != nil {
		return false, err
	}
	return c > 0, nil
}

// FindByMD5 implements models.FunscriptIndexReader.
func (qb *FunscriptIndexStore) FindByMD5(ctx context.Context, md5 string) ([]models.FunscriptIndex, error) {
	q := qb.selectDataset().Prepared(true).Where(qb.table().Col("md5").Eq(md5))
	return qb.getMany(ctx, q)
}

// FindBySceneID implements models.FunscriptIndexReader.
func (qb *FunscriptIndexStore) FindBySceneID(ctx context.Context, sceneID int) ([]models.FunscriptIndex, error) {
	q := qb.selectDataset().Prepared(true).Where(qb.table().Col("scene_id").Eq(sceneID))
	return qb.getMany(ctx, q)
}

// UnmatchedRows implements models.FunscriptIndexReader.
func (qb *FunscriptIndexStore) UnmatchedRows(ctx context.Context) ([]models.FunscriptIndex, error) {
	q := qb.selectDataset().Where(qb.table().Col("scene_id").IsNull())
	return qb.getMany(ctx, q)
}

func (qb *FunscriptIndexStore) getMany(ctx context.Context, q *goqu.SelectDataset) ([]models.FunscriptIndex, error) {
	const single = false
	var ret []models.FunscriptIndex
	if err := queryFunc(ctx, q, single, func(rows *sqlx.Rows) error {
		var r funscriptIndexRow
		if err := rows.StructScan(&r); err != nil {
			return err
		}
		ret = append(ret, r.resolve())
		return nil
	}); err != nil {
		return nil, err
	}
	return ret, nil
}

var _ models.FunscriptIndexReaderWriter = (*FunscriptIndexStore)(nil)
