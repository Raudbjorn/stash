package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
)

type recordingSceneFingerprintFinder struct {
	fingerprints  []models.Fingerprints
	results       [][]*models.ScrapedScene
	query         string
	searchResults []*models.ScrapedScene
}

func (f *recordingSceneFingerprintFinder) FindScenesByFingerprints(_ context.Context, fingerprints []models.Fingerprints) ([][]*models.ScrapedScene, error) {
	f.fingerprints = fingerprints
	return f.results, nil
}

func (f *recordingSceneFingerprintFinder) QueryScene(_ context.Context, query string) ([]*models.ScrapedScene, error) {
	f.query = query
	return f.searchResults, nil
}

func TestSceneMetadataFingerprintDiscoveryPlansRemoteSceneID(t *testing.T) {
	remoteID := "remote-scene"
	remoteTitle := "Remote Title"
	finder := &recordingSceneFingerprintFinder{results: [][]*models.ScrapedScene{{{
		RemoteSiteID: &remoteID,
		Title:        &remoteTitle,
	}}}}
	endpoint := "https://box.example/graphql"
	job := &analyzeSceneMetadataJob{
		configuredStashBoxes: []*models.StashBox{{Endpoint: endpoint}},
		sceneFingerprintFinders: []configuredSceneFingerprintFinder{{
			Endpoint: endpoint,
			Finder:   finder,
		}},
	}
	scene := models.NewScene()
	scene.StashIDs = models.NewRelatedStashIDs([]models.StashID{})
	primary := &models.VideoFile{BaseFile: &models.BaseFile{Fingerprints: models.Fingerprints{
		{Type: models.FingerprintTypeMD5, Fingerprint: "0123456789abcdef"},
		{Type: models.FingerprintTypeOshash, Fingerprint: "fedcba9876543210"},
		{Type: models.FingerprintTypePhash, Fingerprint: int64(42)},
	}}}

	candidates, chosen, chosenScene, err := job.discoverRemoteScenes(context.Background(), &scene, primary)
	if err != nil {
		t.Fatal(err)
	}
	if len(finder.fingerprints) != 1 || len(finder.fingerprints[0]) != 3 {
		t.Fatalf("fingerprint query = %+v", finder.fingerprints)
	}
	if len(candidates) != 1 || chosen == nil || chosenScene == nil || chosen.RemoteID != remoteID || chosen.Provenance != "fingerprint" {
		t.Fatalf("candidates = %+v chosen = %+v", candidates, chosen)
	}
}

func TestSceneMetadataAcceptedRemoteMatchWritesStashID(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Fingerprint Scene")
	endpoint := "https://box.example/graphql"
	remoteID := "remote-scene"
	plan := &AnalysisPlan{
		RunID: "fingerprint-run", SceneID: scene.ID,
		StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
		State:          SceneMetadataPlanAccepted, CreatedAt: time.Now().UTC(),
		RemoteCandidates: []RemoteSceneCandidate{{
			Endpoint: endpoint, RemoteID: remoteID,
			Provenance: "fingerprint", Decision: metadata.SceneCandidateAccept,
		}},
		Suggested: []SuggestedField{{
			Kind: sceneMetadataActionRemoteScene,
			PayloadJSON: actionPayload(sceneMetadataActionPayload{
				Endpoint: endpoint,
				RemoteID: remoteID,
			}),
			State: SceneMetadataPlanAccepted,
		}},
	}
	job := &analyzeSceneMetadataJob{repository: repository}
	if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if err := applySceneMetadataPlan(context.Background(), repository, plan.RunID, scene.ID); err != nil {
		t.Fatal(err)
	}
	var stashIDs []models.StashID
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		fresh, err := repository.Scene.Find(ctx, scene.ID)
		if err != nil {
			return err
		}
		if err := fresh.LoadStashIDs(ctx, repository.Scene); err != nil {
			return err
		}
		stashIDs = fresh.StashIDs.List()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(stashIDs) != 1 || stashIDs[0].Endpoint != endpoint || stashIDs[0].StashID != remoteID {
		t.Fatalf("scene stash IDs = %+v", stashIDs)
	}
}

func TestSceneMetadataBlockedSearchRanksStrongMatchForReview(t *testing.T) {
	remoteID := "search-match"
	title := "Specific Feature Title"
	date := "2025-04-03"
	duration := 120
	performerName := "Performer One"
	studioStoredID := "1"
	finder := &recordingSceneFingerprintFinder{searchResults: []*models.ScrapedScene{{
		RemoteSiteID: &remoteID,
		Title:        &title,
		Date:         &date,
		Duration:     &duration,
		Studio:       &models.ScrapedStudio{StoredID: &studioStoredID, Name: "Studio One"},
		Performers:   []*models.ScrapedPerformer{{Name: &performerName}},
	}}}
	endpoint := "https://box.example/graphql"
	job := &analyzeSceneMetadataJob{
		configuredStashBoxes: []*models.StashBox{{Endpoint: endpoint}},
		sceneFingerprintFinders: []configuredSceneFingerprintFinder{{
			Endpoint: endpoint, Finder: finder,
		}},
		studioRecords:    []metadata.NamedAliases{{ID: 1, Name: "Studio One"}},
		performerRecords: []metadata.NamedAliases{{ID: 1, Name: performerName}},
	}
	scene := models.NewScene()
	scene.Title = title
	parsedDate, _ := models.ParseDate(date)
	scene.Date = &parsedDate
	scene.StashIDs = models.NewRelatedStashIDs([]models.StashID{})
	studioID := 1
	scene.StudioID = &studioID
	scene.PerformerIDs = models.NewRelatedIDs([]int{1})
	primary := &models.VideoFile{BaseFile: &models.BaseFile{}, Duration: float64(duration)}

	candidates, chosen, _, err := job.discoverRemoteScenes(context.Background(), &scene, primary)
	if err != nil {
		t.Fatal(err)
	}
	if finder.query == "" {
		t.Fatal("blocked search was not attempted")
	}
	if len(candidates) != 1 || chosen != nil || candidates[0].Decision != metadata.SceneCandidateReview ||
		candidates[0].Score < 1 || len(candidates[0].Contributions) != 4 {
		t.Fatalf("candidates = %+v chosen = %+v", candidates, chosen)
	}
}

func TestSceneMetadataBlockedSearchLeavesCloseMatchesForReview(t *testing.T) {
	title := "Specific Feature Title"
	date := "2025-04-03"
	duration := 120
	firstID, secondID := "first", "second"
	finder := &recordingSceneFingerprintFinder{searchResults: []*models.ScrapedScene{
		{RemoteSiteID: &firstID, Title: &title, Date: &date, Duration: &duration},
		{RemoteSiteID: &secondID, Title: &title, Date: &date, Duration: &duration},
	}}
	endpoint := "https://box.example/graphql"
	job := &analyzeSceneMetadataJob{
		configuredStashBoxes: []*models.StashBox{{Endpoint: endpoint}},
		sceneFingerprintFinders: []configuredSceneFingerprintFinder{{
			Endpoint: endpoint, Finder: finder,
		}},
	}
	scene := models.NewScene()
	scene.Title = title
	parsedDate, _ := models.ParseDate(date)
	scene.Date = &parsedDate
	scene.StashIDs = models.NewRelatedStashIDs([]models.StashID{})
	primary := &models.VideoFile{BaseFile: &models.BaseFile{}, Duration: float64(duration)}

	candidates, chosen, _, err := job.discoverRemoteScenes(context.Background(), &scene, primary)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || chosen != nil {
		t.Fatalf("candidates = %+v chosen = %+v", candidates, chosen)
	}
}

func TestStaleSceneHashIncludesActionDependentRelationshipsAndFileMetadata(t *testing.T) {
	scene := models.NewScene()
	scene.Title = "Planned Scene"
	groupIndex := 1
	groups := []models.GroupsScenes{{GroupID: 10, SceneIndex: &groupIndex}}
	stashIDs := []models.StashID{{Endpoint: "https://box.example/graphql", StashID: "remote"}}
	file := &models.VideoFile{
		BaseFile:       &models.BaseFile{ID: 20},
		Title:          "Container Title",
		Comment:        "Comment",
		Encoder:        "Encoder",
		Tags:           map[string]string{"key": "value"},
		CreationTime:   time.Unix(100, 0).UTC(),
		MetadataProbed: true,
	}
	original := staleSceneHash(&scene, nil, groups, stashIDs, file)

	changedIndex := 2
	changedGroups := []models.GroupsScenes{{GroupID: 10, SceneIndex: &changedIndex}}
	if got := staleSceneHash(&scene, nil, changedGroups, stashIDs, file); got == original {
		t.Fatal("group edit did not change stale-scene hash")
	}
	changedStashIDs := []models.StashID{{Endpoint: "https://box.example/graphql", StashID: "new-remote"}}
	if got := staleSceneHash(&scene, nil, groups, changedStashIDs, file); got == original {
		t.Fatal("stash ID edit did not change stale-scene hash")
	}
	changedFile := *file
	changedFile.Title = "Updated Container Title"
	if got := staleSceneHash(&scene, nil, groups, stashIDs, &changedFile); got == original {
		t.Fatal("file metadata edit did not change stale-scene hash")
	}
}

func TestSceneMetadataPlansLatestOnlyReturnsNewestRun(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Latest Plan")
	oldTitle, newTitle := "Old suggestion", "New suggestion"
	job := &analyzeSceneMetadataJob{repository: repository}
	for _, plan := range []*AnalysisPlan{
		{
			RunID: "older-run", SceneID: scene.ID,
			StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
			State:          SceneMetadataPlanProposed,
			CreatedAt:      time.Now().UTC().Add(-time.Hour),
			Suggested: []SuggestedField{{
				Kind:        sceneMetadataActionTitle,
				PayloadJSON: actionPayload(sceneMetadataActionPayload{Title: &oldTitle}),
				State:       SceneMetadataPlanProposed,
			}},
		},
		{
			RunID: "newer-run", SceneID: scene.ID,
			StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
			State:          SceneMetadataPlanProposed,
			CreatedAt:      time.Now().UTC(),
			Suggested: []SuggestedField{{
				Kind:        sceneMetadataActionTitle,
				PayloadJSON: actionPayload(sceneMetadataActionPayload{Title: &newTitle}),
				State:       SceneMetadataPlanProposed,
			}},
		},
	} {
		if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{Repository: repository}
	plans, err := manager.SceneMetadataPlans(
		context.Background(), []string{fmt.Sprint(scene.ID)}, nil, nil, true, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].RunID != "newer-run" || len(plans[0].Suggested) != 1 {
		t.Fatalf("latest plans = %+v", plans)
	}
	var payload sceneMetadataActionPayload
	if err := json.Unmarshal([]byte(plans[0].Suggested[0].PayloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Title == nil || *payload.Title != newTitle {
		t.Fatalf("latest action payload = %+v", payload)
	}

	appliedAt := time.Now().UTC()
	if err := repository.WithTxn(context.Background(), func(ctx context.Context) error {
		return repository.SceneMetadataPlan.SetSceneMetadataPlanState(
			ctx,
			"newer-run",
			scene.ID,
			string(SceneMetadataPlanApplied),
			&appliedAt,
		)
	}); err != nil {
		t.Fatal(err)
	}
	plans, err = manager.SceneMetadataPlans(
		context.Background(), []string{fmt.Sprint(scene.ID)}, nil, nil, true, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].RunID != "older-run" {
		t.Fatalf("latest actionable plans after applying newer run = %+v", plans)
	}

	if err := repository.WithTxn(context.Background(), func(ctx context.Context) error {
		return repository.SceneMetadataPlan.SetSceneMetadataPlanState(
			ctx,
			"older-run",
			scene.ID,
			string(SceneMetadataPlanApplied),
			&appliedAt,
		)
	}); err != nil {
		t.Fatal(err)
	}
	plans, err = manager.SceneMetadataPlans(
		context.Background(), []string{fmt.Sprint(scene.ID)}, nil, nil, true, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 0 {
		t.Fatalf("latest actionable plans after applying every run = %+v", plans)
	}
}

func TestSceneMetadataPlansRunIDFilterIsolatesRuns(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "RunID Filter Scene")
	job := &analyzeSceneMetadataJob{repository: repository}
	for _, runID := range []string{"alpha-run", "beta-run"} {
		if err := job.persistAnalysisPlan(context.Background(), &AnalysisPlan{
			RunID: runID, SceneID: scene.ID,
			StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
			State:          SceneMetadataPlanProposed,
			CreatedAt:      time.Now().UTC(),
			Suggested: []SuggestedField{{
				Kind:        sceneMetadataActionTitle,
				PayloadJSON: actionPayload(sceneMetadataActionPayload{}),
				State:       SceneMetadataPlanProposed,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{Repository: repository}
	target := "beta-run"
	plans, err := manager.SceneMetadataPlans(
		context.Background(), []string{fmt.Sprint(scene.ID)}, &target, nil, false, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].RunID != "beta-run" {
		t.Fatalf("run-id filtered plans = %+v", plans)
	}
}

func TestSceneMetadataPlansHistoryRespectsLimit(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Limit Scene")
	job := &analyzeSceneMetadataJob{repository: repository}
	for index := 0; index < 5; index++ {
		if err := job.persistAnalysisPlan(context.Background(), &AnalysisPlan{
			RunID: fmt.Sprintf("run-%02d", index), SceneID: scene.ID,
			StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
			State:          SceneMetadataPlanProposed,
			CreatedAt:      time.Now().UTC().Add(time.Duration(index) * time.Minute),
			Suggested: []SuggestedField{{
				Kind:        sceneMetadataActionTitle,
				PayloadJSON: actionPayload(sceneMetadataActionPayload{}),
				State:       SceneMetadataPlanProposed,
			}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	manager := &Manager{Repository: repository}
	limit := 2
	plans, err := manager.SceneMetadataPlans(
		context.Background(), []string{fmt.Sprint(scene.ID)}, nil, nil, false, &limit,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 2 {
		t.Fatalf("limit-filtered plans = %+v", plans)
	}
	if plans[0].RunID != "run-04" || plans[1].RunID != "run-03" {
		t.Fatalf("limit order = %+v", plans)
	}
}

func TestSceneMetadataPlanPersistDeduplicatesCreatePerformer(t *testing.T) {
	repository := newTestRepository(t)
	scene := createTestScene(t, repository, "Dedup Scene")
	job := &analyzeSceneMetadataJob{repository: repository}
	performerA := models.NewPerformer()
	performerA.Name = "Dedup Performer"
	performerB := models.NewPerformer()
	performerB.Name = "  dedup performer "
	plan := &AnalysisPlan{
		RunID: "dedup-run", SceneID: scene.ID,
		StaleSceneHash: staleSceneHash(scene, nil, nil, nil, nil),
		State:          SceneMetadataPlanProposed,
		CreatedAt:      time.Now().UTC(),
		Suggested: []SuggestedField{
			{Kind: sceneMetadataActionCreatePerformer, PayloadJSON: actionPayload(sceneMetadataActionPayload{Performer: &performerA}), State: SceneMetadataPlanProposed},
			{Kind: sceneMetadataActionCreatePerformer, PayloadJSON: actionPayload(sceneMetadataActionPayload{Performer: &performerB}), State: SceneMetadataPlanProposed},
		},
	}
	if err := job.persistAnalysisPlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	var actions []models.SceneMetadataPlanActionRecord
	if err := repository.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		actions, err = repository.SceneMetadataPlan.FindSceneMetadataPlanActions(
			ctx, "dedup-run", scene.ID,
		)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 {
		t.Fatalf("persisted actions = %+v, want exactly one deduplicate", actions)
	}
	if plan.Suggested != nil && len(plan.Suggested) != 1 {
		t.Fatalf("plan suggestions not deduped in place: %+v", plan.Suggested)
	}
}
