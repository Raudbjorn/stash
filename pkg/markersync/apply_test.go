package markersync

import (
	"context"
	"testing"
)

// fakeTagResolver assigns a stable id per distinct tag name, creating new ids
// on demand. It records how many times each name was resolved so caching can be
// verified.
type fakeTagResolver struct {
	ids   map[string]int
	next  int
	calls map[string]int
}

func newFakeTagResolver(seed map[string]int) *fakeTagResolver {
	f := &fakeTagResolver{
		ids:   map[string]int{},
		next:  1,
		calls: map[string]int{},
	}
	for name, id := range seed {
		f.ids[name] = id
		if id >= f.next {
			f.next = id + 1
		}
	}
	return f
}

func (f *fakeTagResolver) ResolveOrCreate(_ context.Context, name string) (int, error) {
	f.calls[name]++
	if id, ok := f.ids[name]; ok {
		return id, nil
	}
	id := f.next
	f.next++
	f.ids[name] = id
	return id, nil
}

// fakeMarkerWriter records created/deleted markers against a fixed set of
// existing markers.
type fakeMarkerWriter struct {
	existing []ExistingMarker
	created  []createdMarker
	deleted  []int
	nextID   int
}

type createdMarker struct {
	sceneID      int
	title        string
	seconds      float64
	endSeconds   *float64
	primaryTagID int
	extraTagIDs  []int
}

func (w *fakeMarkerWriter) ExistingForScene(_ context.Context, _ int) ([]ExistingMarker, error) {
	return w.existing, nil
}

func (w *fakeMarkerWriter) CreateMarker(_ context.Context, sceneID int, title string, seconds float64, endSeconds *float64, primaryTagID int, extraTagIDs []int) (int, error) {
	w.nextID++
	w.created = append(w.created, createdMarker{
		sceneID:      sceneID,
		title:        title,
		seconds:      seconds,
		endSeconds:   endSeconds,
		primaryTagID: primaryTagID,
		extraTagIDs:  extraTagIDs,
	})
	return 1000 + w.nextID, nil
}

func (w *fakeMarkerWriter) DeleteMarker(_ context.Context, id int) error {
	w.deleted = append(w.deleted, id)
	return nil
}

func TestApply(t *testing.T) {
	const tagA = 1
	const tagB = 2
	seed := map[string]int{"A": tagA, "B": tagB}

	// Existing marker: 100s, primary tag A (id 1).
	baseExisting := func() []ExistingMarker {
		return []ExistingMarker{{ID: 42, Seconds: 100, PrimaryTagID: tagA}}
	}

	tests := []struct {
		name        string
		mode        string
		candidate   MarkerCandidate
		wantCreated int
		wantSkipped int
		wantOverwr  int
		wantDeleted []int
	}{
		{
			name:        "dup same tag within window - skip mode",
			mode:        ModeSkip,
			candidate:   MarkerCandidate{Title: "c", PrimaryTag: "A", Seconds: 108},
			wantSkipped: 1,
		},
		{
			name:        "dup same tag within window - merge mode",
			mode:        ModeMerge,
			candidate:   MarkerCandidate{Title: "c", PrimaryTag: "A", Seconds: 108},
			wantSkipped: 1,
		},
		{
			name:        "dup same tag within window - overwrite mode deletes+creates",
			mode:        ModeOverwrite,
			candidate:   MarkerCandidate{Title: "c", PrimaryTag: "A", Seconds: 108},
			wantOverwr:  1,
			wantDeleted: []int{42},
		},
		{
			name:        "same time different tag is not a dup (tag aware)",
			mode:        ModeSkip,
			candidate:   MarkerCandidate{Title: "c", PrimaryTag: "B", Seconds: 108},
			wantCreated: 1,
		},
		{
			name:        "same tag outside window is not a dup",
			mode:        ModeSkip,
			candidate:   MarkerCandidate{Title: "c", PrimaryTag: "A", Seconds: 130},
			wantCreated: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &fakeMarkerWriter{existing: baseExisting()}
			tags := newFakeTagResolver(seed)

			res, err := Apply(context.Background(), w, tags, 7,
				[]MarkerCandidate{tt.candidate},
				ApplyOptions{Tolerance: 15, Mode: tt.mode, TagAware: true})
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if res.Created != tt.wantCreated {
				t.Errorf("Created = %d, want %d", res.Created, tt.wantCreated)
			}
			if res.Skipped != tt.wantSkipped {
				t.Errorf("Skipped = %d, want %d", res.Skipped, tt.wantSkipped)
			}
			if res.Overwritten != tt.wantOverwr {
				t.Errorf("Overwritten = %d, want %d", res.Overwritten, tt.wantOverwr)
			}

			gotDeleted := w.deleted
			if len(gotDeleted) != len(tt.wantDeleted) {
				t.Errorf("deleted = %v, want %v", gotDeleted, tt.wantDeleted)
			} else {
				for i := range gotDeleted {
					if gotDeleted[i] != tt.wantDeleted[i] {
						t.Errorf("deleted = %v, want %v", gotDeleted, tt.wantDeleted)
						break
					}
				}
			}

			// CreatedIDs should track every created marker (including overwrite
			// replacements).
			wantCreatedMarkers := tt.wantCreated + tt.wantOverwr
			if len(res.CreatedIDs) != wantCreatedMarkers {
				t.Errorf("len(CreatedIDs) = %d, want %d", len(res.CreatedIDs), wantCreatedMarkers)
			}
			if len(w.created) != wantCreatedMarkers {
				t.Errorf("writer created %d markers, want %d", len(w.created), wantCreatedMarkers)
			}
		})
	}
}

func TestApply_MultiCandidateBatch(t *testing.T) {
	seed := map[string]int{"A": 1}

	t.Run("two near-duplicate candidates dedup against each other (skip)", func(t *testing.T) {
		w := &fakeMarkerWriter{} // empty DB
		tags := newFakeTagResolver(seed)

		res, err := Apply(context.Background(), w, tags, 7,
			[]MarkerCandidate{
				{Title: "first", PrimaryTag: "A", Seconds: 100},
				{Title: "second", PrimaryTag: "A", Seconds: 105}, // within tolerance of first
			},
			ApplyOptions{Tolerance: 15, Mode: ModeSkip, TagAware: true})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if res.Created != 1 || res.Skipped != 1 {
			t.Errorf("Created=%d Skipped=%d, want 1/1 (second dedups against the first, created earlier in the batch)", res.Created, res.Skipped)
		}
		if len(w.created) != 1 {
			t.Errorf("writer created %d markers, want 1", len(w.created))
		}
	})

	t.Run("consecutive overwrite candidates do not double-delete the original", func(t *testing.T) {
		w := &fakeMarkerWriter{existing: []ExistingMarker{{ID: 42, Seconds: 100, PrimaryTagID: 1}}}
		tags := newFakeTagResolver(seed)

		res, err := Apply(context.Background(), w, tags, 7,
			[]MarkerCandidate{
				{Title: "c1", PrimaryTag: "A", Seconds: 105},
				{Title: "c2", PrimaryTag: "A", Seconds: 108},
			},
			ApplyOptions{Tolerance: 15, Mode: ModeOverwrite, TagAware: true})
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if res.Overwritten != 2 {
			t.Errorf("Overwritten = %d, want 2", res.Overwritten)
		}
		// c1 deletes original 42 and creates 1001; c2 must match the freshly
		// created 1001, NOT delete the already-removed 42 a second time.
		want := []int{42, 1001}
		if len(w.deleted) != len(want) || w.deleted[0] != want[0] || w.deleted[1] != want[1] {
			t.Errorf("deleted = %v, want %v (no double-delete of the original)", w.deleted, want)
		}
		if len(w.created) != 2 {
			t.Errorf("writer created %d markers, want 2", len(w.created))
		}
	})
}

func TestApply_SkipTags(t *testing.T) {
	w := &fakeMarkerWriter{}
	tags := newFakeTagResolver(nil)

	res, err := Apply(context.Background(), w, tags, 7,
		[]MarkerCandidate{
			{Title: "keep", PrimaryTag: "Good", Seconds: 10},
			{Title: "drop", PrimaryTag: "Banned", Seconds: 20},
		},
		ApplyOptions{Tolerance: 15, Mode: ModeSkip, TagAware: true, SkipTags: []string{"Banned"}})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Created != 1 {
		t.Errorf("Created = %d, want 1", res.Created)
	}
	if len(w.created) != 1 || w.created[0].title != "keep" {
		t.Errorf("expected only 'keep' created, got %+v", w.created)
	}
}

func TestApply_TagAwareFalse(t *testing.T) {
	// With TagAware=false, a same-time different-tag candidate IS a duplicate.
	w := &fakeMarkerWriter{existing: []ExistingMarker{{ID: 42, Seconds: 100, PrimaryTagID: 1}}}
	tags := newFakeTagResolver(map[string]int{"A": 1, "B": 2})

	res, err := Apply(context.Background(), w, tags, 7,
		[]MarkerCandidate{{Title: "c", PrimaryTag: "B", Seconds: 108}},
		ApplyOptions{Tolerance: 15, Mode: ModeSkip, TagAware: false})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Skipped != 1 || res.Created != 0 {
		t.Errorf("TagAware=false: Created=%d Skipped=%d, want 0/1", res.Created, res.Skipped)
	}
}

func TestApply_ExtraTagsExcludePrimary(t *testing.T) {
	w := &fakeMarkerWriter{}
	tags := newFakeTagResolver(map[string]int{"Primary": 1, "Extra": 2})

	_, err := Apply(context.Background(), w, tags, 7,
		[]MarkerCandidate{{
			Title:      "c",
			PrimaryTag: "Primary",
			Seconds:    10,
			ExtraTags:  []string{"Extra", "Primary", "Extra"}, // primary + dup should be filtered
		}},
		ApplyOptions{Tolerance: 15, Mode: ModeSkip, TagAware: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(w.created) != 1 {
		t.Fatalf("got %d created, want 1", len(w.created))
	}
	got := w.created[0].extraTagIDs
	if len(got) != 1 || got[0] != 2 {
		t.Errorf("extraTagIDs = %v, want [2] (primary + dups excluded)", got)
	}
}

func TestApply_DefaultsToleranceAndMode(t *testing.T) {
	// A NEGATIVE tolerance -> DefaultTolerance (15); Mode "" -> skip. (A 0
	// tolerance is now a deliberate exact-match window, tested separately.)
	w := &fakeMarkerWriter{existing: []ExistingMarker{{ID: 42, Seconds: 100, PrimaryTagID: 1}}}
	tags := newFakeTagResolver(map[string]int{"A": 1})

	res, err := Apply(context.Background(), w, tags, 7,
		[]MarkerCandidate{{Title: "c", PrimaryTag: "A", Seconds: 110}}, // within default 15
		ApplyOptions{TagAware: true, Tolerance: -1})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (default tolerance/mode)", res.Skipped)
	}
}

func TestApply_ZeroToleranceExactMatch(t *testing.T) {
	// Tolerance 0 means exact match: a candidate 14s away from an existing marker
	// is NOT a duplicate and must be created.
	w := &fakeMarkerWriter{existing: []ExistingMarker{{ID: 42, Seconds: 100, PrimaryTagID: 1}}}
	tags := newFakeTagResolver(map[string]int{"A": 1})

	res, err := Apply(context.Background(), w, tags, 7,
		[]MarkerCandidate{{Title: "c", PrimaryTag: "A", Seconds: 114}}, // 14s away
		ApplyOptions{Tolerance: 0, Mode: ModeSkip, TagAware: true})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Created != 1 || res.Skipped != 0 {
		t.Errorf("Tolerance 0: Created=%d Skipped=%d, want 1/0 (14s apart is NOT a dup)", res.Created, res.Skipped)
	}

	// And a candidate landing on the exact same second IS a duplicate.
	w2 := &fakeMarkerWriter{existing: []ExistingMarker{{ID: 42, Seconds: 100, PrimaryTagID: 1}}}
	res2, err := Apply(context.Background(), w2, newFakeTagResolver(map[string]int{"A": 1}), 7,
		[]MarkerCandidate{{Title: "c", PrimaryTag: "A", Seconds: 100}},
		ApplyOptions{Tolerance: 0, Mode: ModeSkip, TagAware: true})
	if err != nil {
		t.Fatalf("Apply (exact): %v", err)
	}
	if res2.Skipped != 1 || res2.Created != 0 {
		t.Errorf("Tolerance 0 exact: Created=%d Skipped=%d, want 0/1", res2.Created, res2.Skipped)
	}
}

func TestCachingTagResolver(t *testing.T) {
	inner := newFakeTagResolver(map[string]int{"A": 1})
	c := NewCachingTagResolver(inner)

	for i := 0; i < 3; i++ {
		id, err := c.ResolveOrCreate(context.Background(), "A")
		if err != nil {
			t.Fatalf("ResolveOrCreate: %v", err)
		}
		if id != 1 {
			t.Fatalf("id = %d, want 1", id)
		}
	}

	if inner.calls["A"] != 1 {
		t.Errorf("inner resolver called %d times, want 1 (cached)", inner.calls["A"])
	}
}
