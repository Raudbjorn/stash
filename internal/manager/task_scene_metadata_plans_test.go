package manager

import (
	"context"
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
			Provenance: "fingerprint", Decision: "accept",
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
