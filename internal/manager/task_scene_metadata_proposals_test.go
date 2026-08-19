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
		StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil), State: SceneMetadataPlanProposed,
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
	if record := findSceneMetadataPlanRecord(t, repository, plan.RunID, scene.ID); record.State != string(SceneMetadataPlanAccepted) {
		t.Fatalf("reviewed plan state = %q, want accepted", record.State)
	}
	forged := identifiers[0][:len(identifiers[0])-1] + "0"
	if _, err := manager.AcceptSceneMetadataProposalActions(context.Background(), plan.RunID, scene.ID, []string{forged}); err == nil {
		t.Fatal("forged action identifier was accepted")
	}
	if _, err := manager.ApplySceneMetadataPlans(context.Background(), plan.RunID, []string{strconv.Itoa(scene.ID)}); err != nil {
		t.Fatal(err)
	}
	if record := findSceneMetadataPlanRecord(t, repository, plan.RunID, scene.ID); record.State != string(SceneMetadataPlanApplied) {
		t.Fatalf("applied plan state = %q, want applied", record.State)
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
		StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
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
		StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
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

func TestSceneMetadataProposalFullRejectionUpdatesPlanState(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Rejected Scene")
	title := "Unwanted Title"
	plan := &AnalysisPlan{
		RunID: "rejected-run", SceneID: scene.ID,
		StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
		State:          SceneMetadataPlanProposed, CreatedAt: time.Now().UTC(),
		Suggested: []SuggestedField{{
			Kind: sceneMetadataActionTitle, PayloadJSON: actionPayload(sceneMetadataActionPayload{Title: &title}),
			State: SceneMetadataPlanProposed,
		}},
	}
	job := &analyzeSceneMetadataJob{repository: repository}
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
	actionID := sceneMetadataActionIdentifier(plan.RunID, scene.ID, actions[0].ID)
	manager := &Manager{Repository: repository}
	if _, err := manager.RejectSceneMetadataProposalActions(context.Background(), plan.RunID, scene.ID, []string{actionID}); err != nil {
		t.Fatal(err)
	}
	if record := findSceneMetadataPlanRecord(t, repository, plan.RunID, scene.ID); record.State != string(SceneMetadataPlanRejected) {
		t.Fatalf("rejected plan state = %q, want rejected", record.State)
	}
}

func TestSceneMetadataMixedPerformerActionsKeepCreatedPerformerLinked(t *testing.T) {
	repository := newTestRepository(t)
	existingID := createTestPerformer(t, repository, "Existing Performer", "", "", nil, nil)
	scene := createTestScene(t, repository, "Mixed Cast")
	if err := repository.WithTxn(context.Background(), func(ctx context.Context) error {
		_, err := repository.Scene.UpdatePartial(ctx, scene.ID, models.ScenePartial{
			PerformerIDs: &models.UpdateIDs{IDs: []int{existingID}, Mode: models.RelationshipUpdateModeSet},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	proposed := models.NewPerformer()
	proposed.Name = "New Performer"
	plan := &AnalysisPlan{
		RunID: "mixed-performers-run", SceneID: scene.ID,
		StaleSceneHash: staleSceneHash(scene, []int{existingID}, nil, nil, nil),
		State:          SceneMetadataPlanAccepted, CreatedAt: time.Now().UTC(),
		Suggested: []SuggestedField{
			{
				Kind: sceneMetadataActionCreatePerformer, PayloadJSON: actionPayload(sceneMetadataActionPayload{Performer: &proposed}),
				State: SceneMetadataPlanAccepted,
			},
			{
				Kind: sceneMetadataActionPerformerIDs, PayloadJSON: actionPayload(sceneMetadataActionPayload{PerformerIDs: []int{existingID}}),
				State: SceneMetadataPlanAccepted,
			},
		},
	}
	job := &analyzeSceneMetadataJob{repository: repository}
	if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := applySceneMetadataPlan(context.Background(), repository, plan.RunID, scene.ID); err != nil {
		t.Fatal(err)
	}
	var performerIDs []int
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		fresh, err := repository.Scene.Find(ctx, scene.ID)
		if err != nil {
			return err
		}
		if err := fresh.LoadPerformerIDs(ctx, repository.Scene); err != nil {
			return err
		}
		performerIDs = append([]int(nil), fresh.PerformerIDs.List()...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(performerIDs) != 2 || !containsInt(performerIDs, existingID) {
		t.Fatalf("scene performer IDs = %v, want existing and newly created performers", performerIDs)
	}
}

func TestSceneMetadataBulkApplyRollsBackAllScenes(t *testing.T) {
	repository := newTestRepository(t)
	first := createTestScene(t, repository, "First Original")
	second := createTestScene(t, repository, "Second Original")
	firstTitle := "First Changed"
	missingStudioID := 999999
	runID := "bulk-atomic-run"
	job := &analyzeSceneMetadataJob{repository: repository}
	plans := []*AnalysisPlan{
		{
			RunID: runID, SceneID: first.ID,
			StaleSceneHash: staleSceneHash(first, nil, nil, nil, nil),
			State:          SceneMetadataPlanAccepted, CreatedAt: time.Now().UTC(),
			Suggested: []SuggestedField{{
				Kind: sceneMetadataActionTitle, PayloadJSON: actionPayload(sceneMetadataActionPayload{Title: &firstTitle}),
				State: SceneMetadataPlanAccepted,
			}},
		},
		{
			RunID: runID, SceneID: second.ID,
			StaleSceneHash: staleSceneHash(second, nil, nil, nil, nil),
			State:          SceneMetadataPlanAccepted, CreatedAt: time.Now().UTC(),
			Suggested: []SuggestedField{{
				Kind: sceneMetadataActionStudioID, PayloadJSON: actionPayload(sceneMetadataActionPayload{StudioID: &missingStudioID}),
				State: SceneMetadataPlanAccepted,
			}},
		},
	}
	for _, plan := range plans {
		if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{Repository: repository}
	if _, err := manager.ApplySceneMetadataPlans(
		context.Background(), runID, []string{strconv.Itoa(first.ID), strconv.Itoa(second.ID)},
	); err == nil {
		t.Fatal("bulk apply unexpectedly succeeded")
	}
	var fresh *models.Scene
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		fresh, err = repository.Scene.Find(ctx, first.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if fresh.Title != "First Original" {
		t.Fatalf("first scene title = %q after failed bulk apply", fresh.Title)
	}
	if record := findSceneMetadataPlanRecord(t, repository, runID, first.ID); record.State != string(SceneMetadataPlanAccepted) {
		t.Fatalf("first plan state = %q after rollback, want accepted", record.State)
	}
}

func findSceneMetadataPlanRecord(t *testing.T, repository models.Repository, runID string, sceneID int) *models.SceneMetadataPlanRecord {
	t.Helper()
	var record *models.SceneMetadataPlanRecord
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		record, err = repository.SceneMetadataPlan.FindSceneMetadataPlan(ctx, runID, sceneID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if record == nil {
		t.Fatalf("scene metadata plan %s/%d not found", runID, sceneID)
	}
	return record
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
