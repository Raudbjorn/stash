package sqlite

import (
	"context"
	"time"

	"github.com/doug-martin/goqu/v9"
	"github.com/jmoiron/sqlx"
	"github.com/stashapp/stash/pkg/models"
)

const (
	sceneMetadataPlansTable       = "scene_metadata_plans"
	sceneMetadataPlanActionsTable = "scene_metadata_plan_actions"
)

type sceneMetadataPlanRow struct {
	RunID            string     `db:"run_id" goqu:"run_id"`
	SceneID          int        `db:"scene_id" goqu:"scene_id"`
	ProposalJSON     string     `db:"proposal_json" goqu:"proposal_json"`
	ModelFingerprint string     `db:"model_fingerprint" goqu:"model_fingerprint"`
	PolicyVersion    string     `db:"policy_version" goqu:"policy_version"`
	State            string     `db:"state" goqu:"state"`
	CreatedAt        time.Time  `db:"created_at" goqu:"created_at"`
	AppliedAt        *time.Time `db:"applied_at" goqu:"applied_at"`
}

type sceneMetadataPlanActionRow struct {
	ID          int    `db:"id" goqu:"skipinsert"`
	RunID       string `db:"run_id" goqu:"run_id"`
	SceneID     int    `db:"scene_id" goqu:"scene_id"`
	Kind        string `db:"kind" goqu:"kind"`
	PayloadJSON string `db:"payload_json" goqu:"payload_json"`
	State       string `db:"state" goqu:"state"`
	ReasonCodes string `db:"reason_codes" goqu:"reason_codes"`
}

type SceneMetadataPlanStore struct{}

func NewSceneMetadataPlanStore() *SceneMetadataPlanStore {
	return &SceneMetadataPlanStore{}
}

func (s *SceneMetadataPlanStore) UpsertSceneMetadataPlan(ctx context.Context, plan *models.SceneMetadataPlanRecord, actions []models.SceneMetadataPlanActionRecord) error {
	row := sceneMetadataPlanRow{
		RunID: plan.RunID, SceneID: plan.SceneID, ProposalJSON: plan.ProposalJSON,
		ModelFingerprint: plan.ModelFingerprint, PolicyVersion: plan.PolicyVersion,
		State: plan.State, CreatedAt: plan.CreatedAt, AppliedAt: plan.AppliedAt,
	}
	insert := dialect.Insert(sceneMetadataPlansTable).Prepared(true).Rows(row).
		OnConflict(goqu.DoUpdate("run_id,scene_id", goqu.Record{
			"proposal_json": row.ProposalJSON, "model_fingerprint": row.ModelFingerprint,
			"policy_version": row.PolicyVersion, "state": row.State,
			"created_at": row.CreatedAt, "applied_at": row.AppliedAt,
		}))
	if _, err := exec(ctx, insert); err != nil {
		return err
	}
	if _, err := exec(ctx, dialect.Delete(sceneMetadataPlanActionsTable).Prepared(true).
		Where(goqu.Ex{"run_id": plan.RunID, "scene_id": plan.SceneID})); err != nil {
		return err
	}
	for index := range actions {
		action := &actions[index]
		action.RunID, action.SceneID = plan.RunID, plan.SceneID
		row := sceneMetadataPlanActionRow{
			RunID: action.RunID, SceneID: action.SceneID, Kind: action.Kind,
			PayloadJSON: action.PayloadJSON, State: action.State, ReasonCodes: action.ReasonCodes,
		}
		result, err := exec(ctx, dialect.Insert(sceneMetadataPlanActionsTable).Prepared(true).Rows(row))
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		action.ID = int(id)
	}
	return nil
}

func (s *SceneMetadataPlanStore) FindSceneMetadataPlans(ctx context.Context, sceneIDs []int, runID *string, state *string, limit *int) ([]models.SceneMetadataPlanRecord, error) {
	query := dialect.From(sceneMetadataPlansTable).Select(goqu.Star()).Order(goqu.I("created_at").Desc(), goqu.I("scene_id").Asc())
	if len(sceneIDs) > 0 {
		query = query.Where(goqu.I("scene_id").In(sceneIDs))
	}
	if runID != nil {
		query = query.Where(goqu.I("run_id").Eq(*runID))
	}
	if state != nil {
		query = query.Where(goqu.I("state").Eq(*state))
	}
	if limit != nil && *limit > 0 {
		query = query.Limit(uint(*limit))
	}
	var result []models.SceneMetadataPlanRecord
	if err := queryFunc(ctx, query.Prepared(true), false, func(rows *sqlx.Rows) error {
		var row sceneMetadataPlanRow
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		result = append(result, models.SceneMetadataPlanRecord{
			RunID: row.RunID, SceneID: row.SceneID, ProposalJSON: row.ProposalJSON,
			ModelFingerprint: row.ModelFingerprint, PolicyVersion: row.PolicyVersion,
			State: row.State, CreatedAt: row.CreatedAt, AppliedAt: row.AppliedAt,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *SceneMetadataPlanStore) FindLatestSceneMetadataPlans(ctx context.Context, sceneIDs []int, runID *string, state *string) ([]models.SceneMetadataPlanRecord, error) {
	latestRun := dialect.From(sceneMetadataPlansTable).
		Select(goqu.I("run_id")).
		Order(goqu.I("created_at").Desc()).
		Limit(1)
	if len(sceneIDs) > 0 {
		latestRun = latestRun.Where(goqu.I("scene_id").In(sceneIDs))
	}
	if runID != nil {
		latestRun = latestRun.Where(goqu.I("run_id").Eq(*runID))
	}
	if state != nil {
		latestRun = latestRun.Where(goqu.I("state").Eq(*state))
	} else {
		latestRun = latestRun.Where(
			goqu.I("state").In("proposed", "accepted"),
		)
	}
	query := dialect.From(sceneMetadataPlansTable).Select(goqu.Star()).
		Where(goqu.I("run_id").Eq(latestRun)).
		Order(goqu.I("scene_id").Asc())
	if len(sceneIDs) > 0 {
		query = query.Where(goqu.I("scene_id").In(sceneIDs))
	}
	if runID != nil {
		query = query.Where(goqu.I("run_id").Eq(*runID))
	}
	if state != nil {
		query = query.Where(goqu.I("state").Eq(*state))
	}
	var result []models.SceneMetadataPlanRecord
	if err := queryFunc(ctx, query.Prepared(true), false, func(rows *sqlx.Rows) error {
		var row sceneMetadataPlanRow
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		result = append(result, models.SceneMetadataPlanRecord{
			RunID: row.RunID, SceneID: row.SceneID, ProposalJSON: row.ProposalJSON,
			ModelFingerprint: row.ModelFingerprint, PolicyVersion: row.PolicyVersion,
			State: row.State, CreatedAt: row.CreatedAt, AppliedAt: row.AppliedAt,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SceneMetadataPlanStore) FindSceneMetadataPlan(ctx context.Context, runID string, sceneID int) (*models.SceneMetadataPlanRecord, error) {
	query := dialect.From(sceneMetadataPlansTable).Select(goqu.Star()).Prepared(true).
		Where(goqu.Ex{"run_id": runID, "scene_id": sceneID})
	var result *models.SceneMetadataPlanRecord
	if err := queryFunc(ctx, query, true, func(rows *sqlx.Rows) error {
		var row sceneMetadataPlanRow
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		result = &models.SceneMetadataPlanRecord{
			RunID: row.RunID, SceneID: row.SceneID, ProposalJSON: row.ProposalJSON,
			ModelFingerprint: row.ModelFingerprint, PolicyVersion: row.PolicyVersion,
			State: row.State, CreatedAt: row.CreatedAt, AppliedAt: row.AppliedAt,
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SceneMetadataPlanStore) FindSceneMetadataPlanActions(ctx context.Context, runID string, sceneID int) ([]models.SceneMetadataPlanActionRecord, error) {
	query := dialect.From(sceneMetadataPlanActionsTable).Select(goqu.Star()).Prepared(true).
		Where(goqu.Ex{"run_id": runID, "scene_id": sceneID}).Order(goqu.I("id").Asc())
	var result []models.SceneMetadataPlanActionRecord
	if err := queryFunc(ctx, query, false, func(rows *sqlx.Rows) error {
		var row sceneMetadataPlanActionRow
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		result = append(result, models.SceneMetadataPlanActionRecord{
			ID: row.ID, RunID: row.RunID, SceneID: row.SceneID, Kind: row.Kind,
			PayloadJSON: row.PayloadJSON, State: row.State, ReasonCodes: row.ReasonCodes,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}
func (s *SceneMetadataPlanStore) FindSceneMetadataPlanActionsForRuns(ctx context.Context, runIDs []string, sceneIDs []int) ([]models.SceneMetadataPlanActionRecord, error) {
	if len(runIDs) == 0 || len(sceneIDs) == 0 {
		return nil, nil
	}
	query := dialect.From(sceneMetadataPlanActionsTable).Select(goqu.Star()).Prepared(true).
		Where(goqu.I("run_id").In(runIDs), goqu.I("scene_id").In(sceneIDs)).
		Order(goqu.I("run_id").Asc(), goqu.I("scene_id").Asc(), goqu.I("id").Asc())
	var result []models.SceneMetadataPlanActionRecord
	if err := queryFunc(ctx, query, false, func(rows *sqlx.Rows) error {
		var row sceneMetadataPlanActionRow
		if err := rows.StructScan(&row); err != nil {
			return err
		}
		result = append(result, models.SceneMetadataPlanActionRecord{
			ID: row.ID, RunID: row.RunID, SceneID: row.SceneID, Kind: row.Kind,
			PayloadJSON: row.PayloadJSON, State: row.State, ReasonCodes: row.ReasonCodes,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *SceneMetadataPlanStore) SetSceneMetadataPlanState(ctx context.Context, runID string, sceneID int, state string, appliedAt *time.Time) error {
	_, err := exec(ctx, dialect.Update(sceneMetadataPlansTable).Prepared(true).
		Set(goqu.Record{"state": state, "applied_at": appliedAt}).
		Where(goqu.Ex{"run_id": runID, "scene_id": sceneID}))
	return err
}

func (s *SceneMetadataPlanStore) SetSceneMetadataPlanActionStates(ctx context.Context, runID string, sceneID int, ids []int, fromState, toState string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	result, err := exec(ctx, dialect.Update(sceneMetadataPlanActionsTable).Prepared(true).
		Set(goqu.Record{"state": toState}).Where(
		goqu.Ex{"run_id": runID, "scene_id": sceneID, "state": fromState},
		goqu.I("id").In(ids),
	))
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	return int(count), err
}
func (s *SceneMetadataPlanStore) CreateSceneMetadataPlanAction(ctx context.Context, action *models.SceneMetadataPlanActionRecord) error {
	row := sceneMetadataPlanActionRow{
		RunID: action.RunID, SceneID: action.SceneID, Kind: action.Kind,
		PayloadJSON: action.PayloadJSON, State: action.State, ReasonCodes: action.ReasonCodes,
	}
	result, err := exec(ctx, dialect.Insert(sceneMetadataPlanActionsTable).Prepared(true).Rows(row))
	if err != nil {
		return err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	action.ID = int(id)
	return nil
}

var _ models.SceneMetadataPlanReaderWriter = (*SceneMetadataPlanStore)(nil)
