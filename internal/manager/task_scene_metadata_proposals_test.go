package manager

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
)

func TestSceneMetadataProposalReviewAndApply(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Original Title")
	createdAt := time.Now().UTC()
	newTitle := "Reviewed Title"
	newDate := "2025-04-03"
	studioID := 999999
	job := &analyzeSceneMetadataJob{repository: repository}
	plan := &AnalysisPlan{
		RunID: "review-run", SceneID: scene.ID,
		StaleSceneHash: staleSceneHash(scene, nil), State: SceneMetadataPlanProposed,
		CreatedAt: createdAt,
		Suggested: []SuggestedField{
			{Kind: sceneMetadataActionTitle, PayloadJSON: actionPayload(sceneMetadataActionPayload{Title: &newTitle}), State: SceneMetadataPlanProposed},
			{Kind: sceneMetadataActionDate, PayloadJSON: actionPayload(sceneMetadataActionPayload{Date: &newDate}), State: SceneMetadataPlanProposed},
			{Kind: sceneMetadataActionStudioID, PayloadJSON: actionPayload(sceneMetadataActionPayload{StudioID: &studioID}), State: SceneMetadataPlanProposed},
		},
	}
	if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}

	var actions []models.SceneMetadataPlanActionRecord
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		actions, err = repository.SceneMetadataPlan.FindSceneMetadataPlanActions(ctx, plan.RunID, scene.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 3 {
		t.Fatalf("actions = %d, want 3", len(actions))
	}
	identifiers := make([]string, len(actions))
	for index, action := range actions {
		identifiers[index] = sceneMetadataActionIdentifier(plan.RunID, scene.ID, action.ID)
	}
	manager := &Manager{Repository: repository}
	if _, err := manager.AcceptSceneMetadataProposalActions(context.Background(), plan.RunID, scene.ID, identifiers[:2]); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RejectSceneMetadataProposalActions(context.Background(), plan.RunID, scene.ID, identifiers[2:]); err != nil {
		t.Fatal(err)
	}
	forged := identifiers[0][:len(identifiers[0])-1] + "0"
	if _, err := manager.AcceptSceneMetadataProposalActions(context.Background(), plan.RunID, scene.ID, []string{forged}); err == nil {
		t.Fatal("forged action identifier was accepted")
	}
	if _, err := manager.ApplySceneMetadataPlans(context.Background(), plan.RunID, []string{strconv.Itoa(scene.ID)}); err != nil {
		t.Fatal(err)
	}

	var fresh *models.Scene
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		fresh, err = repository.Scene.Find(ctx, scene.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if fresh.Title != newTitle || fresh.Date == nil || fresh.Date.String() != newDate {
		t.Fatalf("applied scene = title %q date %v", fresh.Title, fresh.Date)
	}
	if fresh.StudioID != nil {
		t.Fatalf("rejected studio action changed scene: %v", fresh.StudioID)
	}
}

func TestAnalyzeSceneMetadata_AtomicityOnCancel(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Atomic Scene")
	proposed := models.NewPerformer()
	proposed.Name = "Rollback Performer"
	missingStudioID := 999999
	plan := &AnalysisPlan{
		RunID: "atomic-run", SceneID: scene.ID,
		StaleSceneHash: staleSceneHash(scene, nil),
		State:          SceneMetadataPlanAccepted, CreatedAt: time.Now().UTC(),
		Suggested: []SuggestedField{
			{
				Kind:        sceneMetadataActionCreatePerformer,
				PayloadJSON: actionPayload(sceneMetadataActionPayload{Performer: &proposed}),
				State:       SceneMetadataPlanAccepted,
			},
			{
				Kind:        sceneMetadataActionStudioID,
				PayloadJSON: actionPayload(sceneMetadataActionPayload{StudioID: &missingStudioID}),
				State:       SceneMetadataPlanAccepted,
			},
		},
	}
	job := &analyzeSceneMetadataJob{repository: repository}
	if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := applySceneMetadataPlan(context.Background(), repository, plan.RunID, scene.ID); err == nil {
		t.Fatal("apply unexpectedly succeeded with a missing studio foreign key")
	}
	if got := performerCount(t, repository); got != 0 {
		t.Fatalf("orphan performer count = %d, want 0 after rollback", got)
	}
}

func TestAnalyzeSceneMetadataRejectsStalePlan(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Planned Title")
	suggestedTitle := "Suggested Title"
	plan := &AnalysisPlan{
		RunID: "stale-run", SceneID: scene.ID,
		StaleSceneHash: staleSceneHash(scene, nil),
		State:          SceneMetadataPlanAccepted, CreatedAt: time.Now().UTC(),
		Suggested: []SuggestedField{{
			Kind:        sceneMetadataActionTitle,
			PayloadJSON: actionPayload(sceneMetadataActionPayload{Title: &suggestedTitle}),
			State:       SceneMetadataPlanAccepted,
		}},
	}
	job := &analyzeSceneMetadataJob{repository: repository}
	if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := repository.WithTxn(context.Background(), func(ctx context.Context) error {
		_, err := repository.Scene.UpdatePartial(ctx, scene.ID, models.ScenePartial{
			Title: models.NewOptionalString("User Edit"),
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := applySceneMetadataPlan(context.Background(), repository, plan.RunID, scene.ID); err != nil {
		t.Fatal(err)
	}

	var record *models.SceneMetadataPlanRecord
	var fresh *models.Scene
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		record, err = repository.SceneMetadataPlan.FindSceneMetadataPlan(ctx, plan.RunID, scene.ID)
		if err != nil {
			return err
		}
		fresh, err = repository.Scene.Find(ctx, scene.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if record == nil || record.State != string(SceneMetadataPlanStale) {
		t.Fatalf("plan state = %+v, want stale", record)
	}
	if fresh.Title != "User Edit" {
		t.Fatalf("stale apply changed title to %q", fresh.Title)
	}
}
