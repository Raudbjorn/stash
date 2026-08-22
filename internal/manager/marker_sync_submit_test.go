package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stashapp/stash/pkg/markersync"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// createTag creates a tag by name and returns its id.
func createTag(t *testing.T, ctx context.Context, r models.Repository, name string) int {
	t.Helper()
	var id int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		tag := models.NewTag()
		tag.Name = name
		if err := r.Tag.Create(ctx, &models.CreateTagInput{Tag: &tag}); err != nil {
			return err
		}
		id = tag.ID
		return nil
	}); err != nil {
		t.Fatalf("creating tag %q: %v", name, err)
	}
	return id
}

// TestMarkerSyncSubmitIntegration exercises buildSceneSubmission against a live
// sqlite database (proving relationship loading and the seconds-preserving
// marker mapping), then submits the result to an httptest server, and finally
// verifies the skip-submit tag excludes a scene from selection.
func TestMarkerSyncSubmitIntegration(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	const endpoint = "https://stashdb.example/graphql"

	// primary tags for markers
	talkingID := createTag(t, ctx, r, "Talking")
	actionID := createTag(t, ctx, r, "Action")
	// a scene-level tag
	amateurID := createTag(t, ctx, r, "Amateur")

	// studio with a stash id
	var studioID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		st := models.NewStudio()
		st.Name = "Studio X"
		st.StashIDs = models.NewRelatedStashIDs([]models.StashID{
			{Endpoint: endpoint, StashID: "studio-1"},
		})
		if err := r.Studio.Create(ctx, &models.CreateStudioInput{Studio: &st}); err != nil {
			return err
		}
		studioID = st.ID
		return nil
	}); err != nil {
		t.Fatalf("creating studio: %v", err)
	}

	// the scene under test: stash id, studio, one scene tag, two markers
	var sceneID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc := models.NewScene()
		sc.Title = "Submit Test Scene"
		sc.Details = "some details"
		d, err := models.ParseDate("2024-05-06")
		if err != nil {
			return err
		}
		sc.Date = &d
		sc.StudioID = &studioID
		sc.StashIDs = models.NewRelatedStashIDs([]models.StashID{
			{Endpoint: endpoint, StashID: "scene-1"},
		})
		sc.TagIDs = models.NewRelatedIDs([]int{amateurID})
		sc.URLs = models.NewRelatedStrings([]string{"https://example/scene"})
		if err := r.Scene.Create(ctx, &sc, nil); err != nil {
			return err
		}
		sceneID = sc.ID

		m1 := models.NewSceneMarker()
		m1.Title = "Intro"
		m1.Seconds = 90 // seconds, must be preserved verbatim
		m1.PrimaryTagID = talkingID
		m1.SceneID = sceneID
		if err := r.SceneMarker.Create(ctx, &m1); err != nil {
			return err
		}

		m2 := models.NewSceneMarker()
		m2.Title = "Action Begins"
		m2.Seconds = 123.456
		m2.PrimaryTagID = actionID
		m2.SceneID = sceneID
		return r.SceneMarker.Create(ctx, &m2)
	}); err != nil {
		t.Fatalf("creating scene: %v", err)
	}

	// --- buildSceneSubmission ---
	var sub markersync.SceneSubmission
	if err := txn.WithReadTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc, err := r.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		sub, err = buildSceneSubmission(ctx, r, sc)
		return err
	}); err != nil {
		t.Fatalf("buildSceneSubmission: %v", err)
	}

	if sub.Title != "Submit Test Scene" {
		t.Errorf("title = %q, want Submit Test Scene", sub.Title)
	}
	if sub.Date != "2024-05-06" {
		t.Errorf("date = %q, want 2024-05-06", sub.Date)
	}
	if len(sub.StashIDs) != 1 || sub.StashIDs[0].StashID != "scene-1" || sub.StashIDs[0].Endpoint != endpoint {
		t.Errorf("stash ids = %+v, want [{%s scene-1}]", sub.StashIDs, endpoint)
	}
	if sub.Studio == nil || sub.Studio.Name != "Studio X" {
		t.Fatalf("studio = %+v, want Studio X", sub.Studio)
	}
	if len(sub.Studio.StashIDs) != 1 || sub.Studio.StashIDs[0].StashID != "studio-1" {
		t.Errorf("studio stash ids = %+v, want studio-1", sub.Studio.StashIDs)
	}
	if len(sub.Tags) != 1 || sub.Tags[0] != "Amateur" {
		t.Errorf("scene tags = %+v, want [Amateur]", sub.Tags)
	}

	if len(sub.Markers) != 2 {
		t.Fatalf("got %d markers, want 2", len(sub.Markers))
	}
	byTag := map[string]markersync.SubmissionMarker{}
	for _, m := range sub.Markers {
		byTag[m.PrimaryTag] = m
	}
	// CRITICAL: seconds preserved (native), NOT multiplied by 1000.
	if m := byTag["Talking"]; m.Seconds != 90 || m.Title != "Intro" {
		t.Errorf("Talking marker = %+v, want seconds 90 / title Intro", m)
	}
	if m := byTag["Action"]; m.Seconds != 123.456 || m.Title != "Action Begins" {
		t.Errorf("Action marker = %+v, want seconds 123.456 / title Action Begins", m)
	}

	// --- SubmitScene POSTs the expected JSON to /submit-stash ---
	var (
		gotPath string
		body    map[string]any
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		gotPath = req.URL.Path
		_ = json.NewDecoder(req.Body).Decode(&body)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	tt := markersync.NewTimestampTradeSource(markersync.TimestampTradeOptions{
		Enabled: true,
		BaseURL: srv.URL,
	})
	if err := tt.SubmitScene(ctx, sub); err != nil {
		t.Fatalf("SubmitScene: %v", err)
	}
	if gotPath != "/submit-stash" {
		t.Errorf("path = %q, want /submit-stash", gotPath)
	}
	markersJSON, _ := body["scene_markers"].([]any)
	if len(markersJSON) != 2 {
		t.Fatalf("posted scene_markers = %v, want 2", body["scene_markers"])
	}
	// verify at least one posted marker carries seconds (not milliseconds)
	foundSeconds := false
	for _, mj := range markersJSON {
		mm, _ := mj.(map[string]any)
		if sec, ok := mm["seconds"].(float64); ok && (sec == 90 || sec == 123.456) {
			foundSeconds = true
		}
		if sec, ok := mm["seconds"].(float64); ok && (sec == 90000 || sec == 123456) {
			t.Errorf("posted marker seconds = %v, want seconds NOT milliseconds", sec)
		}
	}
	if !foundSeconds {
		t.Errorf("no posted marker had the expected native-seconds value: %v", markersJSON)
	}

	// --- skip-submit tag excludes a scene from selection ---
	skipTagID := createTag(t, ctx, r, markerSyncSkipSubmitTagName)

	var skippedSceneID int
	if err := txn.WithTxn(ctx, r.TxnManager, func(ctx context.Context) error {
		sc := models.NewScene()
		sc.Title = "Skipped Scene"
		sc.TagIDs = models.NewRelatedIDs([]int{skipTagID})
		if err := r.Scene.Create(ctx, &sc, nil); err != nil {
			return err
		}
		skippedSceneID = sc.ID

		m := models.NewSceneMarker()
		m.Title = "Marker"
		m.Seconds = 5
		m.PrimaryTagID = talkingID
		m.SceneID = skippedSceneID
		return r.SceneMarker.Create(ctx, &m)
	}); err != nil {
		t.Fatalf("creating skipped scene: %v", err)
	}

	mgr := &Manager{Repository: r}
	scenes, err := mgr.markerSyncSubmitScenes(ctx, MarkerSyncSubmitInput{})
	if err != nil {
		t.Fatalf("markerSyncSubmitScenes: %v", err)
	}

	got := map[int]bool{}
	for _, sc := range scenes {
		got[sc.ID] = true
	}
	if !got[sceneID] {
		t.Errorf("scene %d (with markers, no skip tag) was not selected", sceneID)
	}
	if got[skippedSceneID] {
		t.Errorf("scene %d (skip-submit tag) was selected but should be excluded", skippedSceneID)
	}
}
