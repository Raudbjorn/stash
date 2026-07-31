package tagging

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
)

func TestNormalizeEvalRequestCanonicalizesScenesAndDefaultsInterval(t *testing.T) {
	normalized, err := NormalizeVLMEvalRequest(VLMEvalRequest{SceneIDs: []int{3, 1, 3, 2}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized.SceneIDs, []int{1, 2, 3}) || normalized.FrameInterval != llamaprov.DefaultFrameInterval {
		t.Fatalf("normalized=%+v", normalized)
	}
	for name, request := range map[string]VLMEvalRequest{
		"empty scenes":      {},
		"zero scene":        {SceneIDs: []int{0}},
		"negative scene":    {SceneIDs: []int{-1}},
		"negative interval": {SceneIDs: []int{1}, FrameInterval: -1},
		"NaN interval":      {SceneIDs: []int{1}, FrameInterval: math.NaN()},
		"infinite interval": {SceneIDs: []int{1}, FrameInterval: math.Inf(1)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeVLMEvalRequest(request); err == nil {
				t.Fatal("invalid evaluation request was accepted")
			}
		})
	}
}

func TestEvalCancelledHonorsContextAndTaskFlag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(evalCancelled(ctx, nil), context.Canceled) {
		t.Error("cancelled context was ignored")
	}
	if !errors.Is(evalCancelled(context.Background(), func() bool { return true }), context.Canceled) {
		t.Error("task cancellation flag was ignored")
	}
	if err := evalCancelled(context.Background(), func() bool { return false }); err != nil {
		t.Errorf("live evaluation reported cancellation: %v", err)
	}
}

func TestFinishEvalMetricsPreservesUndefinedValues(t *testing.T) {
	labels := []string{"half", "missed", "unmeasured"}
	counts := map[string]*evalCount{
		"half":       {samples: 4, truth: 2, predicted: 2, truePositive: 1},
		"missed":     {samples: 4, truth: 2},
		"unmeasured": {samples: 4, predicted: 2},
	}
	metrics, macro, measured := finishEvalMetrics(labels, counts)
	if measured != 2 || macro == nil || *macro != 0.25 {
		t.Fatalf("measured=%d macro=%v", measured, macro)
	}
	half := metrics["half"]
	if half.Precision == nil || *half.Precision != 0.5 || half.Recall == nil || *half.Recall != 0.5 || half.F1 == nil || *half.F1 != 0.5 {
		t.Errorf("half = %+v", half)
	}
	missed := metrics["missed"]
	if !missed.Measured || missed.Precision != nil || missed.Recall == nil || *missed.Recall != 0 || missed.F1 == nil || *missed.F1 != 0 {
		t.Errorf("missed = %+v", missed)
	}
	unmeasured := metrics["unmeasured"]
	if unmeasured.Measured || unmeasured.Precision != nil || unmeasured.Recall != nil || unmeasured.F1 != nil || unmeasured.Reason == "" {
		t.Errorf("unmeasured = %+v", unmeasured)
	}
	_, macro, measured = finishEvalMetrics([]string{"unmeasured"}, counts)
	if macro != nil || measured != 0 {
		t.Errorf("no-data metrics returned macro=%v measured=%d", macro, measured)
	}
}

func TestVLMEvalResultUsesSnakeCaseAndNullMetrics(t *testing.T) {
	encoded, err := json.Marshal(VLMEvalResult{
		Provider: "llama_vlm", SceneIDs: []int{1}, FrameInterval: 30,
		PerLabel: map[string]VLMEvalLabel{"tag": {Samples: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"scene_ids", "frame_interval", "macro_f1", "per_label"} {
		if _, ok := object[key]; !ok {
			t.Errorf("result omitted JSON key %q: %s", key, encoded)
		}
	}
	if string(object["macro_f1"]) != "null" {
		t.Errorf("undefined macro_f1 = %s", object["macro_f1"])
	}
}

func TestEvaluationActionContextsAndProviderRequirement(t *testing.T) {
	tagger := NewService(models.Repository{}, nil, Config{})
	actions := action.NewRegistry()
	tagger.Register(actions, service.NewRegistry())
	registrations := actions.Get(EvalActionID)
	if len(registrations) != 1 {
		t.Fatalf("registrations = %d", len(registrations))
	}
	registration := registrations[0]
	if registration.Definition.Service != ServiceName || !registration.Definition.DeduplicateSubmissions {
		t.Errorf("definition = %+v", registration.Definition)
	}
	properties := registration.Definition.InputSchema["properties"].(map[string]any)
	frame := properties["frame_interval"].(map[string]any)
	if frame["default"] != llamaprov.DefaultFrameInterval {
		t.Errorf("evaluation interval default = %v", frame["default"])
	}
	id := "4"
	if !registration.Definition.IsApplicable(action.ContextInput{Page: "scenes", EntityID: &id, IsDetailView: true}) {
		t.Error("evaluation is unavailable on a scene detail view")
	}
	if !registration.Definition.IsApplicable(action.ContextInput{Page: "scenes", SelectedIDs: []string{"4", "5"}}) {
		t.Error("evaluation is unavailable for a library selection")
	}
	if registration.Definition.IsApplicable(action.ContextInput{Page: "images", SelectedIDs: []string{"4"}}) {
		t.Error("evaluation is available outside scenes")
	}

	handle := &evalTestHandle{}
	_, err := registration.Handler(context.Background(), action.ContextInput{
		Page: "scenes", EntityID: &id, IsDetailView: true,
	}, nil, handle)
	if !errors.Is(err, ErrVLMProviderRequired) {
		t.Fatalf("non-VLM handler error = %v", err)
	}
	_, err = registration.Handler(context.Background(), action.ContextInput{
		Page: "scenes", EntityID: &id, IsDetailView: true,
	}, map[string]any{"frame_interval": float64(0)}, handle)
	if err == nil {
		t.Fatal("explicit zero interval was accepted")
	}
}

func TestEvaluateVLMExactMetricsProgressCancellationAndReadOnly(t *testing.T) {
	ffmpeg, video := makeVLMTestVideo(t)
	_, markers, db := newTestTrainer(t)
	markers.byScene[1] = []*models.SceneMarker{
		{ID: 1, SceneID: 1, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(2)},
		{ID: 2, SceneID: 1, PrimaryTagID: 2, Seconds: 2, EndSeconds: ptrFloat(6)},
		{ID: 500, SceneID: 1, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(6)},
		{ID: 501, SceneID: 1, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(6)},
	}
	if err := db.RecordMarkers(context.Background(), 1, []store.WrittenMarker{
		{MarkerID: 500, SceneID: 1, Service: ProviderVLM, TagName: "Blowjob", Start: 0},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.RecordMarkers(context.Background(), 1, []store.WrittenMarker{
		{MarkerID: 501, SceneID: 1, Service: "other_provider", TagName: "Blowjob", Start: 0},
	}); err != nil {
		t.Fatal(err)
	}
	classifier := &exactEvalProvider{}
	repo := models.Repository{
		TxnManager: nullTxn{}, SceneMarker: markers,
		Tag:   &tagFinder{byID: map[int]string{1: "Blowjob", 2: "Kissing"}},
		Scene: &evalSceneRepo{sceneID: 1, path: video},
	}
	service := NewService(repo, db, Config{Provider: classifier, FFmpegPath: ffmpeg})
	var progress []VLMEvalProgress
	result, err := service.EvaluateVLM(context.Background(), VLMEvalRequest{
		SceneIDs: []int{1}, FrameInterval: 2,
	}, func(p VLMEvalProgress) { progress = append(progress, p) }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if classifier.calls.Load() != 3 || result.Frames != 3 {
		t.Fatalf("classifier calls=%d result=%+v", classifier.calls.Load(), result)
	}
	wantF1 := 2.0 / 3.0
	if result.MacroF1 == nil || math.Abs(*result.MacroF1-wantF1) > 1e-9 {
		t.Fatalf("macro_f1=%v, want %v", result.MacroF1, wantF1)
	}
	blowjob, kissing := result.PerLabel["Blowjob"], result.PerLabel["Kissing"]
	if blowjob.TruePositives != 1 || blowjob.GroundTruthPositives != 1 || blowjob.PredictedPositives != 2 ||
		kissing.TruePositives != 1 || kissing.GroundTruthPositives != 2 || kissing.PredictedPositives != 1 {
		t.Errorf("metrics: Blowjob=%+v Kissing=%+v", blowjob, kissing)
	}
	if len(progress) == 0 || progress[len(progress)-1].Fraction != 1 ||
		progress[len(progress)-1].Frames != 3 || progress[len(progress)-1].Scenes != 1 {
		t.Errorf("progress = %+v", progress)
	}
	if run, err := db.GetLatestSceneRun(context.Background(), ProviderVLM, 1); err != nil || run != nil {
		t.Errorf("read-only evaluation stored a run: run=%+v err=%v", run, err)
	}
	if len(markers.byScene[1]) != 4 {
		t.Errorf("read-only evaluation changed scene markers: %+v", markers.byScene[1])
	}

	classifier.calls.Store(0)
	_, err = service.EvaluateVLM(context.Background(), VLMEvalRequest{
		SceneIDs: []int{1}, FrameInterval: 2,
	}, nil, func() bool { return classifier.calls.Load() >= 1 })
	if !errors.Is(err, context.Canceled) || classifier.calls.Load() != 1 {
		t.Errorf("cancelled evaluation err=%v calls=%d", err, classifier.calls.Load())
	}
}

type evalSceneRepo struct {
	mocks.SceneReaderWriter
	sceneID int
	path    string
}

func (r *evalSceneRepo) Find(context.Context, int) (*models.Scene, error) {
	return &models.Scene{ID: r.sceneID}, nil
}

func (r *evalSceneRepo) GetFiles(context.Context, int) ([]*models.VideoFile, error) {
	return []*models.VideoFile{{BaseFile: &models.BaseFile{Path: r.path}}}, nil
}

type exactEvalProvider struct{ calls atomic.Int32 }

func (*exactEvalProvider) Name() string { return ProviderVLM }
func (*exactEvalProvider) Capabilities() aitag.Capability {
	return aitag.CapVideo | aitag.CapImages
}
func (*exactEvalProvider) Available(context.Context) error { return nil }
func (*exactEvalProvider) Models(context.Context) ([]aitag.ModelInfo, error) {
	return []aitag.ModelInfo{{Name: "exact-eval", Type: "vision-language"}}, nil
}
func (*exactEvalProvider) AnalyzeVideo(context.Context, string, aitag.Options, aitag.Sink) (*aitag.Result, error) {
	return nil, errors.New("not used")
}
func (*exactEvalProvider) AnalyzeImages(context.Context, []string, aitag.Options) (*aitag.ImageResult, error) {
	return nil, errors.New("not used")
}
func (*exactEvalProvider) Close() error     { return nil }
func (*exactEvalProvider) Labels() []string { return []string{"Blowjob", "Kissing"} }
func (p *exactEvalProvider) ClassifyFrame(context.Context, []byte, int, int) (map[string]bool, error) {
	switch p.calls.Add(1) {
	case 1:
		return map[string]bool{"Blowjob": true, "Kissing": false}, nil
	case 2:
		return map[string]bool{"Blowjob": true, "Kissing": false}, nil
	default:
		return map[string]bool{"Blowjob": false, "Kissing": true}, nil
	}
}

type evalTestHandle struct{}

func (*evalTestHandle) ID() string              { return "eval" }
func (*evalTestHandle) Params() map[string]any  { return nil }
func (*evalTestHandle) Progress(map[string]any) {}
func (*evalTestHandle) Cancelled() bool         { return false }
func (*evalTestHandle) MarkController()         {}
