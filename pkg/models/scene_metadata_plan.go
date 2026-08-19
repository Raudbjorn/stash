package models

import (
	"context"
	"time"
)

// SceneMetadataPlanRecord is the durable envelope for one scene analysis.
// ProposalJSON contains the versioned manager-level plan payload.
type SceneMetadataPlanRecord struct {
	RunID            string
	SceneID          int
	ProposalJSON     string
	ModelFingerprint string
	PolicyVersion    string
	State            string
	CreatedAt        time.Time
	AppliedAt        *time.Time
}

// SceneMetadataPlanActionRecord is one independently reviewable plan action.
type SceneMetadataPlanActionRecord struct {
	ID          int
	RunID       string
	SceneID     int
	Kind        string
	PayloadJSON string
	State       string
	ReasonCodes string
}

type SceneMetadataPlanReader interface {
	FindSceneMetadataPlans(ctx context.Context, sceneIDs []int, runID *string, state *string, limit *int) ([]SceneMetadataPlanRecord, error)
	FindLatestSceneMetadataPlans(ctx context.Context, sceneIDs []int, runID *string, state *string) ([]SceneMetadataPlanRecord, error)
	FindSceneMetadataPlan(ctx context.Context, runID string, sceneID int) (*SceneMetadataPlanRecord, error)
	FindSceneMetadataPlanActions(ctx context.Context, runID string, sceneID int) ([]SceneMetadataPlanActionRecord, error)
	FindSceneMetadataPlanActionsForRuns(ctx context.Context, runIDs []string, sceneIDs []int) ([]SceneMetadataPlanActionRecord, error)
}

type SceneMetadataPlanWriter interface {
	UpsertSceneMetadataPlan(ctx context.Context, plan *SceneMetadataPlanRecord, actions []SceneMetadataPlanActionRecord) error
	CreateSceneMetadataPlanAction(ctx context.Context, action *SceneMetadataPlanActionRecord) error
	SetSceneMetadataPlanState(ctx context.Context, runID string, sceneID int, state string, appliedAt *time.Time) error
	SetSceneMetadataPlanActionStates(ctx context.Context, runID string, sceneID int, ids []int, fromState, toState string) (int, error)
}

type SceneMetadataPlanReaderWriter interface {
	SceneMetadataPlanReader
	SceneMetadataPlanWriter
}
