package tagging

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/models/mocks"
	"github.com/stretchr/testify/mock"
)

// The property under test is the one that makes re-analysis safe to run: a
// second pass replaces exactly the markers this service created, and never a
// marker the user placed by hand.

// fakeMarkerRepo records created and destroyed markers.
type fakeMarkerRepo struct {
	mocks.SceneMarkerReaderWriter

	mu      sync.Mutex
	nextID  int
	created []models.SceneMarker
	destroy []int
	// destroyMissing makes Destroy fail, standing in for a marker the user
	// already deleted.
	destroyMissing map[int]bool
}

func (f *fakeMarkerRepo) Create(ctx context.Context, marker *models.SceneMarker) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	marker.ID = f.nextID
	f.created = append(f.created, *marker)
	return nil
}

func (f *fakeMarkerRepo) Destroy(ctx context.Context, id int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.destroyMissing[id] {
		return errors.New("scene marker not found")
	}
	f.destroy = append(f.destroy, id)
	return nil
}

func (f *fakeMarkerRepo) createdIDs() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, 0, len(f.created))
	for _, m := range f.created {
		out = append(out, m.ID)
	}
	return out
}

// fakeTagRepo resolves a fixed set of tags by name.
type fakeTagRepo struct {
	mocks.TagReaderWriter

	mu      sync.Mutex
	byName  map[string]int
	aliases map[string]int
	created []string
	nextID  int
}

func (f *fakeTagRepo) FindByName(ctx context.Context, name string, nocase bool) (*models.Tag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byName[name]; ok {
		return &models.Tag{ID: id, Name: name}, nil
	}
	return nil, nil
}

func (f *fakeTagRepo) FindByAlias(ctx context.Context, name string, nocase bool) (*models.Tag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.aliases[name]; ok {
		return &models.Tag{ID: id, Name: name}, nil
	}
	return nil, nil
}

func (f *fakeTagRepo) Create(ctx context.Context, input *models.CreateTagInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	input.ID = 1000 + f.nextID
	f.byName[input.Name] = input.ID
	f.created = append(f.created, input.Name)
	return nil
}

// nullTxn runs callbacks directly. The writeback's transaction boundaries are
// Stash's concern; what is under test here is what it decides to write.
type nullTxn struct{}

func (nullTxn) Begin(ctx context.Context, exclusive bool) (context.Context, error) { return ctx, nil }
func (nullTxn) WithDatabase(ctx context.Context) (context.Context, error)          { return ctx, nil }
func (nullTxn) Commit(ctx context.Context) error                                   { return nil }
func (nullTxn) Rollback(ctx context.Context) error                                 { return nil }
func (nullTxn) IsLocked(err error) bool                                            { return false }
func (nullTxn) AddPostCommitHook(ctx context.Context, hook txnHook)                {}
func (nullTxn) AddPostRollbackHook(ctx context.Context, hook txnHook)              {}

type txnHook = func(ctx context.Context) error

func newTestWriter(t *testing.T) (*Writer, *fakeMarkerRepo, *fakeTagRepo, *store.DB) {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, id := range []int{1, 2} {
		if _, err := db.SQL().ExecContext(context.Background(), `
			INSERT INTO ai_model_runs
				(id, service, entity_type, entity_id, started_at, completed_at)
			VALUES (?, 'test', 'scene', 42, 1, 1)`, id); err != nil {
			t.Fatalf("seed model run %d: %v", id, err)
		}
	}

	markers := &fakeMarkerRepo{destroyMissing: map[int]bool{}}
	tags := &fakeTagRepo{byName: map[string]int{}, aliases: map[string]int{}}

	repo := models.Repository{
		TxnManager:  nullTxn{},
		SceneMarker: markers,
		Tag:         tags,
	}
	return NewWriter(repo, db), markers, tags, db
}

func markerFixture(tag string, start, end float64) aitag.Marker {
	return aitag.Marker{Category: "actions", Tag: tag, RenamedTag: tag + "_AI", Start: start, End: end}
}

// A second run must replace exactly what the first created.
func TestWritebackReplacesOnlyItsOwnMarkers(t *testing.T) {
	writer, markerRepo, tagRepo, _ := newTestWriter(t)
	tagRepo.byName["Blowjob_AI"] = 5
	ctx := context.Background()

	first, err := writer.Write(ctx, "native", 42, 1,
		[]aitag.Marker{markerFixture("Blowjob", 0, 30), markerFixture("Blowjob", 60, 90)},
		2, DefaultWritebackOptions())
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if first.Created != 2 || first.Removed != 0 {
		t.Fatalf("first write = %+v, want 2 created and 0 removed", first)
	}

	firstIDs := markerRepo.createdIDs()

	second, err := writer.Write(ctx, "native", 42, 2,
		[]aitag.Marker{markerFixture("Blowjob", 5, 40)},
		2, DefaultWritebackOptions())
	if err != nil {
		t.Fatalf("second write: %v", err)
	}
	if second.Created != 1 {
		t.Errorf("second write created %d, want 1", second.Created)
	}
	if second.Removed != 2 {
		t.Errorf("second write removed %d, want the 2 from the first run", second.Removed)
	}

	// Exactly the first run's markers, nothing else.
	if len(markerRepo.destroy) != 2 {
		t.Fatalf("destroyed %v, want the first run's two markers", markerRepo.destroy)
	}
	destroyed := map[int]bool{}
	for _, id := range markerRepo.destroy {
		destroyed[id] = true
	}
	for _, id := range firstIDs {
		if !destroyed[id] {
			t.Errorf("marker %d from the first run survived", id)
		}
	}
}

// A marker created by a DIFFERENT service must not be removed: two providers
// can coexist, and each owns only what it wrote.
func TestWritebackLeavesOtherServicesAlone(t *testing.T) {
	writer, markerRepo, tagRepo, _ := newTestWriter(t)
	tagRepo.byName["Blowjob_AI"] = 5
	ctx := context.Background()

	if _, err := writer.Write(ctx, "skier_aitagging", 42, 1,
		[]aitag.Marker{markerFixture("Blowjob", 0, 30)}, 2, DefaultWritebackOptions()); err != nil {
		t.Fatal(err)
	}

	if _, err := writer.Write(ctx, "native", 42, 2,
		[]aitag.Marker{markerFixture("Blowjob", 60, 90)}, 2, DefaultWritebackOptions()); err != nil {
		t.Fatal(err)
	}

	if len(markerRepo.destroy) != 0 {
		t.Errorf("the native run destroyed another service's markers: %v", markerRepo.destroy)
	}
	if len(markerRepo.created) != 2 {
		t.Errorf("created %d markers, want 2", len(markerRepo.created))
	}
}

// A marker the user deleted by hand must not make the next run fail.
func TestWritebackToleratesAManuallyDeletedMarker(t *testing.T) {
	writer, markerRepo, tagRepo, _ := newTestWriter(t)
	tagRepo.byName["Blowjob_AI"] = 5
	ctx := context.Background()

	if _, err := writer.Write(ctx, "native", 42, 1,
		[]aitag.Marker{markerFixture("Blowjob", 0, 30)}, 2, DefaultWritebackOptions()); err != nil {
		t.Fatal(err)
	}

	// The user deletes it in the UI; Stash reports it as gone next time.
	for _, id := range markerRepo.createdIDs() {
		markerRepo.destroyMissing[id] = true
	}

	result, err := writer.Write(ctx, "native", 42, 2,
		[]aitag.Marker{markerFixture("Blowjob", 5, 40)}, 2, DefaultWritebackOptions())
	if err != nil {
		t.Fatalf("a manually deleted marker broke the next run: %v", err)
	}
	if result.Created != 1 {
		t.Errorf("created %d, want 1", result.Created)
	}
	// It was not really removed, so it must not be counted as removed.
	if result.Removed != 0 {
		t.Errorf("removed = %d, want 0 for a marker that was already gone", result.Removed)
	}
}

// A label with no Stash tag is reported rather than silently swallowed: it is
// the most common reason a run appears to produce nothing.
func TestMissingTagsAreReported(t *testing.T) {
	writer, markerRepo, tagRepo, _ := newTestWriter(t)
	tagRepo.byName["Known_AI"] = 5
	ctx := context.Background()

	result, err := writer.Write(ctx, "native", 42, 1, []aitag.Marker{
		markerFixture("Known", 0, 30),
		markerFixture("Unknown", 60, 90),
		markerFixture("AlsoUnknown", 120, 150),
	}, 2, DefaultWritebackOptions())
	if err != nil {
		t.Fatal(err)
	}

	if result.Created != 1 {
		t.Errorf("created %d, want only the label with a tag", result.Created)
	}
	if result.SkippedNoTag != 2 {
		t.Errorf("SkippedNoTag = %d, want 2", result.SkippedNoTag)
	}
	if len(result.MissingTags) != 2 {
		t.Fatalf("MissingTags = %v", result.MissingTags)
	}
	// Sorted, so the report is stable between runs.
	if result.MissingTags[0] != "AlsoUnknown_AI" || result.MissingTags[1] != "Unknown_AI" {
		t.Errorf("MissingTags = %v, want them sorted", result.MissingTags)
	}
	if len(markerRepo.created) != 1 {
		t.Errorf("wrote %d markers", len(markerRepo.created))
	}
}

// Creating tags is opt-in: silently populating a user's tag list is hard to
// notice and tedious to undo.
func TestTagCreationIsOptIn(t *testing.T) {
	writer, _, tagRepo, _ := newTestWriter(t)
	ctx := context.Background()

	if _, err := writer.Write(ctx, "native", 42, 1,
		[]aitag.Marker{markerFixture("Novel", 0, 30)}, 2, DefaultWritebackOptions()); err != nil {
		t.Fatal(err)
	}
	if len(tagRepo.created) != 0 {
		t.Errorf("tags were created without being asked: %v", tagRepo.created)
	}

	writer.InvalidateTagCache()
	opts := DefaultWritebackOptions()
	opts.CreateMissingTags = true

	result, err := writer.Write(ctx, "native", 42, 2,
		[]aitag.Marker{markerFixture("Novel", 0, 30)}, 2, opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 {
		t.Errorf("created %d markers, want 1", result.Created)
	}
	if len(tagRepo.created) != 1 || tagRepo.created[0] != "Novel_AI" {
		t.Errorf("created tags = %v, want [Novel_AI]", tagRepo.created)
	}
}

// An alias is how a user maps a generated name onto a tag they already have, so
// it must be honoured before concluding the tag is missing.
func TestTagAliasesResolve(t *testing.T) {
	writer, _, tagRepo, _ := newTestWriter(t)
	tagRepo.aliases["Blowjob_AI"] = 77
	ctx := context.Background()

	result, err := writer.Write(ctx, "native", 42, 1,
		[]aitag.Marker{markerFixture("Blowjob", 0, 30)}, 2, DefaultWritebackOptions())
	if err != nil {
		t.Fatal(err)
	}
	if result.Created != 1 || result.SkippedNoTag != 0 {
		t.Errorf("result = %+v, want the alias to resolve", result)
	}
}

// The marker's end must include the frame interval: a span ending at the last
// matching frame actually covers up to the next sample.
func TestMarkerEndIncludesTheFrameInterval(t *testing.T) {
	writer, markerRepo, tagRepo, _ := newTestWriter(t)
	tagRepo.byName["Blowjob_AI"] = 5
	ctx := context.Background()

	if _, err := writer.Write(ctx, "native", 42, 1,
		[]aitag.Marker{markerFixture("Blowjob", 10, 40)}, 2, DefaultWritebackOptions()); err != nil {
		t.Fatal(err)
	}

	if len(markerRepo.created) != 1 {
		t.Fatal("no marker was created")
	}
	marker := markerRepo.created[0]
	if marker.Seconds != 10 {
		t.Errorf("start = %v, want 10", marker.Seconds)
	}
	if marker.EndSeconds == nil || *marker.EndSeconds != 42 {
		t.Errorf("end = %v, want 42 (40 plus one frame interval)", marker.EndSeconds)
	}
}

// A dry run must compute everything and change nothing, which is what makes it
// safe to preview a re-tune across a library.
func TestDryRunWritesNothing(t *testing.T) {
	writer, markerRepo, tagRepo, db := newTestWriter(t)
	tagRepo.byName["Blowjob_AI"] = 5
	ctx := context.Background()

	if _, err := writer.Write(ctx, "native", 42, 1,
		[]aitag.Marker{markerFixture("Blowjob", 0, 30)}, 2, DefaultWritebackOptions()); err != nil {
		t.Fatal(err)
	}
	before := len(markerRepo.created)

	opts := DefaultWritebackOptions()
	opts.DryRun = true

	result, err := writer.Write(ctx, "native", 42, 2,
		[]aitag.Marker{markerFixture("Blowjob", 5, 40), markerFixture("Blowjob", 100, 140)}, 2, opts)
	if err != nil {
		t.Fatal(err)
	}

	if !result.DryRun {
		t.Error("the result does not report itself as a dry run")
	}
	if result.Created != 2 || result.Removed != 1 {
		t.Errorf("dry run = %+v, want it to predict 2 created and 1 removed", result)
	}
	if len(markerRepo.created) != before {
		t.Error("a dry run created markers")
	}
	if len(markerRepo.destroy) != 0 {
		t.Error("a dry run destroyed markers")
	}

	// And the provenance is untouched, so the real run still knows what to
	// replace.
	previous, err := db.PreviousMarkers(ctx, "native", 42)
	if err != nil {
		t.Fatal(err)
	}
	if len(previous) != 1 {
		t.Errorf("a dry run changed the recorded provenance: %v", previous)
	}
}

var _ = mock.Anything
