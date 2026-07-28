package tagging

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/native"
	"github.com/stashapp/stash/pkg/models"
)

// The trainer turns markers into labels. What is under test is the alignment:
// a frame inside a marker must carry that marker's tag, a frame outside every
// marker must be a negative, and a marker this server generated must not become
// training data for its own successor.

// markerFinder serves a fixed set of markers per scene.
type markerFinder struct {
	fakeMarkerRepo
	byScene map[int][]*models.SceneMarker
}

func (m *markerFinder) FindBySceneID(ctx context.Context, sceneID int) ([]*models.SceneMarker, error) {
	return m.byScene[sceneID], nil
}

// tagFinder resolves tag ids to names.
type tagFinder struct {
	fakeTagRepo
	byID map[int]string
}

func (f *tagFinder) Find(ctx context.Context, id int) (*models.Tag, error) {
	name, ok := f.byID[id]
	if !ok {
		return nil, nil
	}
	return &models.Tag{ID: id, Name: name}, nil
}

func newTestTrainer(t *testing.T) (*Trainer, *markerFinder, *store.DB) {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.SQL().ExecContext(context.Background(), `
		INSERT INTO ai_model_runs
			(id, service, entity_type, entity_id, started_at, completed_at)
		VALUES (1, 'test', 'scene', 1, 1, 1)`); err != nil {
		t.Fatalf("seed model run: %v", err)
	}

	markers := &markerFinder{byScene: map[int][]*models.SceneMarker{}}
	tags := &tagFinder{byID: map[int]string{1: "Blowjob", 2: "Kissing"}}
	tags.byName = map[string]int{}
	tags.aliases = map[string]int{}
	markers.destroyMissing = map[int]bool{}

	repo := models.Repository{
		TxnManager:  nullTxn{},
		SceneMarker: markers,
		Tag:         tags,
	}
	return NewTrainer(repo, db), markers, db
}

func ptrFloat(v float64) *float64 { return &v }

// storeEmbeddings writes vectors that are separable by construction: frames
// inside the marked window point one way and the rest point another, so a head
// that learns anything at all must learn that.
func storeEmbeddings(t *testing.T, db *store.DB, sceneID, frames int, dim int, markedUntil float64) {
	t.Helper()

	var (
		times   []float64
		vectors []float32
	)
	for i := 0; i < frames; i++ {
		at := float64(i) * 2
		times = append(times, at)

		vector := make([]float32, dim)
		if at < markedUntil {
			vector[0] = 1
		} else {
			vector[1] = 1
		}
		vectors = append(vectors, vector...)
	}

	if err := db.StoreEmbeddings(context.Background(), "native", store.StoredEmbeddings{
		SceneID:       sceneID,
		Model:         "siglip2",
		Dim:           dim,
		FrameInterval: 2,
		Times:         times,
		Vectors:       vectors,
	}); err != nil {
		t.Fatalf("StoreEmbeddings: %v", err)
	}
}

func TestTrainerLearnsFromMarkers(t *testing.T) {
	trainer, markers, db := newTestTrainer(t)
	ctx := context.Background()

	// Several scenes, each with the first 100 seconds marked.
	for scene := 1; scene <= 6; scene++ {
		storeEmbeddings(t, db, scene, 100, 8, 100)
		markers.byScene[scene] = []*models.SceneMarker{
			{ID: scene * 100, SceneID: scene, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(100)},
		}
	}

	req := DefaultTrainRequest("native", "siglip2")
	req.Options.MinPositives = 5

	result, err := trainer.Train(ctx, req)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}

	if result.ScenesUsed != 6 {
		t.Errorf("used %d scenes, want 6", result.ScenesUsed)
	}
	if len(result.Labels) != 1 || result.Labels[0] != "Blowjob" {
		t.Fatalf("labels = %v, want [Blowjob]", result.Labels)
	}
	// The data is perfectly separable, so a working trainer must nearly ace it.
	if result.Metrics.MacroF1 < 0.9 {
		t.Errorf("macro F1 = %.3f on separable data", result.Metrics.MacroF1)
	}

	// The head is stored and reloadable, which is what makes it usable in a
	// later analysis without retraining.
	head, err := trainer.LoadHead(ctx, req.Name)
	if err != nil {
		t.Fatalf("LoadHead: %v", err)
	}
	if len(head.Labels) != 1 {
		t.Errorf("reloaded head has labels %v", head.Labels)
	}

	// And it separates the two regions.
	inside := make([]float32, 8)
	inside[0] = 1
	outside := make([]float32, 8)
	outside[1] = 1

	scores := make([]float32, 1)
	if err := head.Score(inside, scores); err != nil {
		t.Fatal(err)
	}
	insideScore := scores[0]
	if err := head.Score(outside, scores); err != nil {
		t.Fatal(err)
	}
	if insideScore <= scores[0] {
		t.Errorf("marked frames scored %.3f, unmarked %.3f; the head learned nothing",
			insideScore, scores[0])
	}
}

// Training on markers this server generated would fit the head to its own
// predecessor's output rather than to anything the user decided.
func TestGeneratedMarkersAreExcluded(t *testing.T) {
	trainer, markers, db := newTestTrainer(t)
	ctx := context.Background()

	storeEmbeddings(t, db, 1, 100, 8, 100)
	markers.byScene[1] = []*models.SceneMarker{
		{ID: 500, SceneID: 1, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(100)},
	}

	// Record marker 500 as generated by a different provider. Human truth must
	// exclude every server-generated marker, not only this trainer's service.
	if err := db.RecordMarkers(ctx, 1, []store.WrittenMarker{{
		MarkerID: 500, SceneID: 1, Service: "llama_vlm", TagName: "Blowjob", Start: 0,
	}}); err != nil {
		t.Fatal(err)
	}

	req := DefaultTrainRequest("native", "siglip2")
	req.Options.MinPositives = 1

	_, err := trainer.Train(ctx, req)
	if err == nil {
		t.Fatal("training proceeded using only markers this server generated")
	}
	// A sentinel rather than a message match, so the API can answer 422 for a
	// fresh installation instead of a 500 that reads like something broke.
	if !errors.Is(err, ErrNoTrainingData) && !errors.Is(err, native.ErrNotEnoughData) {
		t.Errorf("err = %v, want a recognisable no-data error", err)
	}

	// With the exclusion off it trains, which confirms the exclusion is what
	// stopped it rather than something else.
	req.ExcludeGenerated = false
	if _, err := trainer.Train(ctx, req); err != nil {
		t.Errorf("training with generated markers included: %v", err)
	}
}

// Frames inside no marker must become negatives. Without them the head sees
// only positives and learns to answer yes to everything.
func TestUnmarkedFramesBecomeNegatives(t *testing.T) {
	trainer, markers, db := newTestTrainer(t)
	ctx := context.Background()

	for scene := 1; scene <= 4; scene++ {
		storeEmbeddings(t, db, scene, 60, 8, 40)
		markers.byScene[scene] = []*models.SceneMarker{
			{ID: scene * 10, SceneID: scene, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(40)},
		}
	}

	req := DefaultTrainRequest("native", "siglip2")
	req.Options.MinPositives = 5

	result, err := trainer.Train(ctx, req)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}

	// 4 scenes x 60 frames, all kept - the marked 20 and the unmarked 40.
	if result.Metrics.Samples != 240 {
		t.Errorf("samples = %d, want 240; unmarked frames were dropped",
			result.Metrics.Samples)
	}

	// The label must be a minority, which is only true if negatives were kept.
	m := result.Metrics.PerLabel["Blowjob"]
	if m.Positives == 0 {
		t.Fatal("no validation positives")
	}
	if float64(m.Positives) > float64(result.Metrics.ValidationCount)*0.6 {
		t.Errorf("%d of %d validation frames are positive; negatives are missing",
			m.Positives, result.Metrics.ValidationCount)
	}
}

// A marker with no end covers an instant, which would match no sampled frame.
// Giving it a nominal window is what makes it usable as a label at all.
func TestMarkersWithNoEndGetAWindow(t *testing.T) {
	trainer, markers, db := newTestTrainer(t)
	ctx := context.Background()

	for scene := 1; scene <= 8; scene++ {
		storeEmbeddings(t, db, scene, 60, 8, 10)
		markers.byScene[scene] = []*models.SceneMarker{
			{ID: scene * 10, SceneID: scene, PrimaryTagID: 1, Seconds: 0},
		}
	}

	req := DefaultTrainRequest("native", "siglip2")
	req.Options.MinPositives = 5

	result, err := trainer.Train(ctx, req)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	if len(result.Labels) == 0 {
		t.Fatal("an end-less marker produced no labels at all")
	}
}

// Restricting to named tags is how a user trains one label at a time.
func TestIncludeTagsFilters(t *testing.T) {
	trainer, markers, db := newTestTrainer(t)
	ctx := context.Background()

	for scene := 1; scene <= 6; scene++ {
		storeEmbeddings(t, db, scene, 80, 8, 80)
		markers.byScene[scene] = []*models.SceneMarker{
			{ID: scene*10 + 1, SceneID: scene, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(40)},
			{ID: scene*10 + 2, SceneID: scene, PrimaryTagID: 2, Seconds: 40, EndSeconds: ptrFloat(80)},
		}
	}

	req := DefaultTrainRequest("native", "siglip2")
	req.Options.MinPositives = 5
	req.IncludeTags = []string{"kissing"} // deliberately lower case

	result, err := trainer.Train(ctx, req)
	if err != nil {
		t.Fatalf("Train: %v", err)
	}
	if len(result.Labels) != 1 || result.Labels[0] != "Kissing" {
		t.Errorf("labels = %v, want only Kissing", result.Labels)
	}
}

func TestTrainerNeedsEmbeddings(t *testing.T) {
	trainer, _, _ := newTestTrainer(t)

	_, err := trainer.Train(context.Background(), DefaultTrainRequest("native", "siglip2"))
	if err == nil {
		t.Fatal("training with no cached embeddings succeeded")
	}
	if !errors.Is(err, ErrNoTrainingData) {
		t.Errorf("err = %v, want ErrNoTrainingData", err)
	}
	if !strings.Contains(err.Error(), "cached") {
		t.Errorf("the error does not explain what is missing: %v", err)
	}
}

// Vectors from two different models are not comparable; mixing them silently
// would produce a head fitted to noise.
func TestMismatchedDimensionsAreRejected(t *testing.T) {
	trainer, markers, db := newTestTrainer(t)
	ctx := context.Background()

	storeEmbeddings(t, db, 1, 40, 8, 40)
	storeEmbeddings(t, db, 2, 40, 16, 40)
	for scene := 1; scene <= 2; scene++ {
		markers.byScene[scene] = []*models.SceneMarker{
			{ID: scene * 10, SceneID: scene, PrimaryTagID: 1, Seconds: 0, EndSeconds: ptrFloat(40)},
		}
	}

	req := DefaultTrainRequest("native", "siglip2")
	req.Options.MinPositives = 1

	if _, err := trainer.Train(ctx, req); err == nil {
		t.Fatal("embeddings of two different widths were mixed into one training set")
	}
}

var _ = native.DefaultTrainOptions

// Regenerating from a scene with no stored spans must REFUSE rather than
// proceed, because regeneration replaces: producing zero markers would delete
// the ones a previous run correctly created.
//
// This was a live failure, not a hypothetical: the first implementation read
// spans through an accessor that filters to resolved tag ids, so a scene whose
// labels had no matching Stash tags regenerated to nothing and wiped its
// markers.
func TestRegenerateRefusesWhenNothingIsStored(t *testing.T) {
	trainer, _, db := newTestTrainer(t)
	ctx := context.Background()

	rules := aitag.NewRules()
	rules.Categories["actions"] = aitag.CategoryRules{
		"Blowjob": {OriginalTag: "Blowjob", RenamedTag: "Blowjob_AI", MinMarkerDuration: "12", TagThreshold: "0.5"},
	}

	svc := NewService(trainer.repo, db, Config{Rules: rules})

	_, err := svc.RegenerateMarkers(ctx, "native", 12345, DefaultWritebackOptions())
	if err == nil {
		t.Fatal("regenerating a scene with no stored analysis succeeded; it would have deleted its markers")
	}
}
