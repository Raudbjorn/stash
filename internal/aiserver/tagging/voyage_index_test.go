package tagging

import (
	"context"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"reflect"
)

type recordingSegmentIndexer struct {
	sceneID   int
	videoPath string
	duration  float64
}

func (r *recordingSegmentIndexer) IndexScene(_ context.Context, sceneID int, videoPath string, duration float64) (int, error) {
	r.sceneID = sceneID
	r.videoPath = videoPath
	r.duration = duration
	return 3, nil
}

type voyageSceneRepo struct {
	mocks.SceneReaderWriter
}

func (*voyageSceneRepo) Find(context.Context, int) (*models.Scene, error) {
	return &models.Scene{ID: 42}, nil
}

func (*voyageSceneRepo) GetFiles(context.Context, int) ([]*models.VideoFile, error) {
	return []*models.VideoFile{{
		BaseFile: &models.BaseFile{Path: "/library/scene.mp4"},
		Duration: 60,
	}}, nil
}

type completedSceneProvider struct{}

func (*completedSceneProvider) Name() string                    { return ProviderVLM }
func (*completedSceneProvider) Capabilities() aitag.Capability  { return aitag.CapVideo }
func (*completedSceneProvider) Available(context.Context) error { return nil }
func (*completedSceneProvider) Models(context.Context) ([]aitag.ModelInfo, error) {
	return nil, nil
}
func (*completedSceneProvider) AnalyzeVideo(context.Context, string, aitag.Options, aitag.Sink) (*aitag.Result, error) {
	return &aitag.Result{Duration: 90}, nil
}
func (*completedSceneProvider) AnalyzeImages(context.Context, []string, aitag.Options) (*aitag.ImageResult, error) {
	return nil, nil
}
func (*completedSceneProvider) Close() error { return nil }

func TestAnalyzeSceneIndexesVoyageRecommendationSegments(t *testing.T) {
	database := mocks.NewDatabase()
	sceneRepo := &voyageSceneRepo{}
	repo := database.Repository()
	repo.Scene = sceneRepo
	indexer := &recordingSegmentIndexer{}
	service := NewService(repo, nil, Config{
		Provider:       &completedSceneProvider{},
		SegmentIndexer: indexer,
	})

	result, err := service.AnalyzeScene(context.Background(), AnalyzeRequest{
		SceneID:       42,
		SkipWriteback: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.VoyageSegments != 3 || indexer.sceneID != 42 || indexer.videoPath != "/library/scene.mp4" || indexer.duration != 90 {
		t.Fatalf("result=%#v indexer=%#v", result, indexer)
	}
}

type recordingVoyageAnalyzer struct {
	sceneIDs []int
}

func (*recordingVoyageAnalyzer) CanTag() bool { return true }

func (*recordingVoyageAnalyzer) IndexScene(context.Context, int, string, float64) (int, error) {
	return 0, nil
}

func (r *recordingVoyageAnalyzer) AnalyzeVideoTags(_ context.Context, sceneID int, _ string, duration, _ float64, _ aitag.Sink) (*aitag.Result, error) {
	r.sceneIDs = append(r.sceneIDs, sceneID)
	end := duration
	confidence := 0.9
	return &aitag.Result{
		SchemaVersion: 3,
		Duration:      duration,
		Spans: aitag.SpansByCategory{
			"Acts": {"Voyage Match": {{Start: 0, End: &end, Confidence: &confidence}}},
		},
		Metrics: map[string]any{"segments": 1},
	}, nil
}

type voyageActionHandle struct{}

func (voyageActionHandle) ID() string              { return "task" }
func (voyageActionHandle) Params() map[string]any  { return nil }
func (voyageActionHandle) Progress(map[string]any) {}
func (voyageActionHandle) Cancelled() bool         { return false }
func (voyageActionHandle) MarkController()         {}

func TestAnalyzeSceneWithVoyageNeedsNoLocalProvider(t *testing.T) {
	database := mocks.NewDatabase()
	repo := database.Repository()
	repo.Scene = &voyageSceneRepo{}
	analyzer := &recordingVoyageAnalyzer{}
	service := NewService(repo, nil, Config{VoyageAnalyzer: analyzer})

	result, err := service.AnalyzeSceneWithVoyage(context.Background(), AnalyzeRequest{
		SceneID: 42, SkipWriteback: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != recommend.VoyageProviderName || result.Spans != 1 || !reflect.DeepEqual(analyzer.sceneIDs, []int{42}) {
		t.Fatalf("result=%#v sceneIDs=%v", result, analyzer.sceneIDs)
	}
}

func TestVoyageActionProcessesMultipleSelectedScenes(t *testing.T) {
	database := mocks.NewDatabase()
	repo := database.Repository()
	repo.Scene = &voyageSceneRepo{}
	analyzer := &recordingVoyageAnalyzer{}
	service := NewService(repo, nil, Config{VoyageAnalyzer: analyzer})

	result, err := service.handler()(context.Background(), action.ContextInput{
		Page: "scenes", SelectedIDs: []string{"41", "42"},
	}, map[string]any{
		"analysis_service": "voyage", "apply_scene_tags": false,
		"create_missing_tags": false, "store_embeddings": false,
	}, voyageActionHandle{})
	if err != nil {
		t.Fatal(err)
	}
	summary, ok := result.(map[string]any)
	if !ok || summary["scenes"] != 2 || summary["failed"] != 0 {
		t.Fatalf("summary=%#v", result)
	}
	if !reflect.DeepEqual(analyzer.sceneIDs, []int{41, 42}) {
		t.Fatalf("sceneIDs=%v, want [41 42]", analyzer.sceneIDs)
	}
}
