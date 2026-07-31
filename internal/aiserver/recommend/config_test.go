package recommend

import (
	"context"
	"reflect"
	"testing"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func numberField(name string, def float64, min, max *float64) ConfigField {
	return ConfigField{Name: name, Label: name, Type: FieldNumber, Default: def, Min: min, Max: max, Persist: true}
}

// The warning strings are displayed verbatim by the UI, so they are a contract.
func TestValidateConfigClampsAndWarns(t *testing.T) {
	def := Definition{
		ID: "r",
		Config: []ConfigField{
			numberField("threshold", 0.5, f(0), f(1)),
			{Name: "label", Label: "label", Type: FieldText, Default: "hi", Persist: true},
		},
	}

	cases := []struct {
		name         string
		raw          map[string]any
		wantValue    any
		wantWarnings []string
	}{
		{
			name:      "defaults applied when absent",
			raw:       nil,
			wantValue: 0.5,
		},
		{
			name:         "above max clamps",
			raw:          map[string]any{"threshold": 5.0},
			wantValue:    1.0,
			wantWarnings: []string{"config.threshold above max; clamped"},
		},
		{
			name:         "below min clamps",
			raw:          map[string]any{"threshold": -3.0},
			wantValue:    0.0,
			wantWarnings: []string{"config.threshold below min; clamped"},
		},
		{
			name:         "non-numeric falls back to the default",
			raw:          map[string]any{"threshold": "abc"},
			wantValue:    0.5,
			wantWarnings: []string{"config.threshold invalid numeric; using default"},
		},
		{
			// A numeric string is accepted rather than rejected.
			name:      "numeric string is coerced",
			raw:       map[string]any{"threshold": "0.75"},
			wantValue: 0.75,
		},
		{
			name:         "undeclared keys are reported",
			raw:          map[string]any{"nonsense": 1},
			wantValue:    0.5,
			wantWarnings: []string{"config.nonsense ignored (undeclared)"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, warnings := ValidateConfig(def, tc.raw)
			if out["threshold"] != tc.wantValue {
				t.Errorf("threshold = %#v, want %#v", out["threshold"], tc.wantValue)
			}
			if tc.wantWarnings != nil && !reflect.DeepEqual(warnings, tc.wantWarnings) {
				t.Errorf("warnings = %v, want %v", warnings, tc.wantWarnings)
			}
			if tc.wantWarnings == nil && len(warnings) != 0 {
				t.Errorf("unexpected warnings %v", warnings)
			}
			// Every declared field appears, so the recommender never has to
			// check for absence.
			if _, ok := out["label"]; !ok {
				t.Error("declared field missing from the validated config")
			}
		})
	}
}

// A missing required value is reported but does not fail the request.
func TestValidateConfigRequired(t *testing.T) {
	def := Definition{Config: []ConfigField{
		{Name: "seed", Label: "seed", Type: FieldText, Required: true, Persist: true},
	}}

	out, warnings := ValidateConfig(def, nil)
	if len(warnings) != 1 || warnings[0] != "config.seed required but missing" {
		t.Errorf("warnings = %v", warnings)
	}
	if _, present := out["seed"]; !present {
		t.Error("required field should still be present, as nil")
	}
}

// A recommender with no declared config passes values through untouched.
func TestValidateConfigWithoutSpec(t *testing.T) {
	raw := map[string]any{"anything": 1}
	out, warnings := ValidateConfig(Definition{}, raw)
	if !reflect.DeepEqual(out, raw) || len(warnings) != 0 {
		t.Errorf("out = %v warnings = %v", out, warnings)
	}
}

// Transient fields must not be restored on the next visit.
func TestPersistableConfig(t *testing.T) {
	def := Definition{Config: []ConfigField{
		{Name: "keep", Type: FieldNumber, Persist: true},
		{Name: "search", Type: FieldSearch, Persist: false},
	}}

	got := PersistableConfig(def, map[string]any{"keep": 1.0, "search": "beach"})
	if _, present := got["search"]; present {
		t.Error("a non-persistent field was stored")
	}
	if got["keep"] != 1.0 {
		t.Errorf("persistable field lost: %v", got)
	}
}

// The two pagination modes: trust a recommender that paginated itself, and
// slice for one that did not.
func TestApplyPagination(t *testing.T) {
	scenes := make([]SceneModel, 10)
	for idx := range scenes {
		scenes[idx] = SceneModel{ID: idx}
	}

	t.Run("server slices when the recommender reports nothing", func(t *testing.T) {
		page, meta := ApplyPagination(Result{Scenes: scenes}, 2, i(3))
		if len(page) != 3 || page[0].ID != 2 {
			t.Errorf("page = %v", page)
		}
		if meta.Total != 10 || !meta.HasMore || meta.NextOffset == nil || *meta.NextOffset != 5 {
			t.Errorf("meta = %+v", meta)
		}
	})

	t.Run("last page reports no more", func(t *testing.T) {
		_, meta := ApplyPagination(Result{Scenes: scenes}, 8, i(5))
		if meta.HasMore || meta.NextOffset != nil {
			t.Errorf("meta = %+v, want the end of the results", meta)
		}
	})

	t.Run("recommender-reported total is trusted", func(t *testing.T) {
		page, meta := ApplyPagination(Result{Scenes: scenes[:3], Total: i(100)}, 0, i(3))
		if len(page) != 3 {
			t.Errorf("the page was re-sliced: %v", page)
		}
		if meta.Total != 100 || !meta.HasMore {
			t.Errorf("meta = %+v", meta)
		}
	})

	t.Run("total is floored at what was returned", func(t *testing.T) {
		// A recommender that under-reports must not contradict its own page.
		_, meta := ApplyPagination(Result{Scenes: scenes, Total: i(2)}, 0, i(10))
		if meta.Total < len(scenes) {
			t.Errorf("total = %d, want at least %d", meta.Total, len(scenes))
		}
	})

	t.Run("offset beyond the end yields an empty page", func(t *testing.T) {
		page, meta := ApplyPagination(Result{Scenes: scenes}, 500, i(10))
		if len(page) != 0 || meta.HasMore {
			t.Errorf("page = %v meta = %+v", page, meta)
		}
	})

	t.Run("nil scenes serialise as an empty page", func(t *testing.T) {
		page, _ := ApplyPagination(Result{}, 0, nil)
		if page == nil {
			t.Error("page is nil; it must serialise as []")
		}
	})
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	handler := func(ctx context.Context, req Request) (Result, error) { return Result{}, nil }

	def := Definition{ID: "tfidf", Label: "TF-IDF", Contexts: []Context{ContextGlobalFeed}}
	if err := r.Register(def, handler, "plugin-a"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Ids are unique, unlike actions where variants share one.
	if err := r.Register(def, handler, "plugin-a"); err == nil {
		t.Error("duplicate registration should fail")
	}

	if got := r.ListForContext(ContextGlobalFeed); len(got) != 1 {
		t.Errorf("global feed = %v", got)
	}
	if got := r.ListForContext(ContextSimilarScene); len(got) != 0 {
		t.Errorf("similar scene = %v, want none", got)
	}
	// Must be [] not nil so the endpoint serialises correctly.
	if r.ListForContext(ContextSimilarScene) == nil {
		t.Error("ListForContext returned nil")
	}

	r.UnregisterOwner("plugin-a")
	if _, ok := r.Get("tfidf"); ok {
		t.Error("recommender survived its plugin being unloaded")
	}
}
