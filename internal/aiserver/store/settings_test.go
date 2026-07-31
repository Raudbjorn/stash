package store

import (
	"context"
	"errors"
	"testing"
)

func TestSeedSystemSettings(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SeedSystemSettings(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := db.ListSettings(ctx, SystemPluginName)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != len(SystemSettingDefs) {
		t.Fatalf("seeded %d settings, want %d", len(got), len(SystemSettingDefs))
	}

	// Insertion order is part of the contract: the API returns settings ordered
	// by id and the frontend renders them in that order.
	for i, def := range SystemSettingDefs {
		if got[i].Key != def.Key {
			t.Errorf("setting %d = %q, want %q (declaration order not preserved)", i, got[i].Key, def.Key)
		}
	}

	// Nothing that only made sense out-of-process should have survived.
	for _, s := range got {
		switch s.Key {
		case "STASH_URL", "STASH_API_KEY", "STASH_DB_PATH", "PATH_MAPPINGS",
			"UI_SHARED_API_KEY", "TASK_LOOP_INTERVAL", "TASK_DEBUG":
			t.Errorf("setting %q should not exist in-process", s.Key)
		}
	}
}

func TestSeedIsIdempotentAndPreservesValues(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if err := db.SeedSystemSettings(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := db.SetSystemSetting(ctx, "SEGMENT_MERGE_GAP_SECONDS", 2.5); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Re-seeding happens on every startup and must not clobber user values.
	if err := db.SeedSystemSettings(ctx); err != nil {
		t.Fatalf("re-seed: %v", err)
	}

	got, err := db.GetSetting(ctx, SystemPluginName, "SEGMENT_MERGE_GAP_SECONDS")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Effective() != 2.5 {
		t.Errorf("re-seed clobbered the value: got %v, want 2.5", got.Effective())
	}

	all, err := db.ListSettings(ctx, SystemPluginName)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != len(SystemSettingDefs) {
		t.Errorf("re-seed duplicated rows: %d settings", len(all))
	}
}

func TestEffectiveFallsBackToDefault(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.SeedSystemSettings(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	s, err := db.GetSetting(ctx, SystemPluginName, "INTERACTION_MIN_SESSION_MINUTES")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if s.Value.Valid {
		t.Error("a freshly seeded setting should have no explicit value")
	}
	if s.Effective() != 10.0 {
		t.Errorf("Effective() = %v, want the default 10", s.Effective())
	}

	// Setting then clearing must fall back to the default again.
	if _, err := db.SetSystemSetting(ctx, "INTERACTION_MIN_SESSION_MINUTES", 30); err != nil {
		t.Fatalf("set: %v", err)
	}
	if v := db.SettingFloat(ctx, "INTERACTION_MIN_SESSION_MINUTES", -1); v != 30 {
		t.Errorf("SettingFloat = %v, want 30", v)
	}

	if _, err := db.SetSystemSetting(ctx, "INTERACTION_MIN_SESSION_MINUTES", nil); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if v := db.SettingFloat(ctx, "INTERACTION_MIN_SESSION_MINUTES", -1); v != 10 {
		t.Errorf("after reset SettingFloat = %v, want the default 10", v)
	}
}

// The accepted inputs and error codes must match the Python API exactly, since
// the frontend parses the codes out of the response.
func TestSettingCoercion(t *testing.T) {
	selectOpts := JSONOf([]any{"a", "b"})

	cases := []struct {
		name    string
		typ     string
		opts    JSONText[[]any]
		in      any
		want    any
		wantErr error
	}{
		{name: "number from float", typ: SettingTypeNumber, in: 2.5, want: 2.5},
		{name: "number from int", typ: SettingTypeNumber, in: 3, want: 3.0},
		{name: "number from string", typ: SettingTypeNumber, in: "4.25", want: 4.25},
		{name: "number rejects text", typ: SettingTypeNumber, in: "abc", wantErr: ErrInvalidNumber},
		{name: "number rejects map", typ: SettingTypeNumber, in: map[string]any{}, wantErr: ErrInvalidNumber},

		{name: "boolean passthrough", typ: SettingTypeBoolean, in: true, want: true},
		{name: "boolean from number", typ: SettingTypeBoolean, in: 1, want: true},
		{name: "boolean from zero", typ: SettingTypeBoolean, in: 0, want: false},
		{name: "boolean from string", typ: SettingTypeBoolean, in: "TRUE", want: true},
		{name: "boolean rejects other text", typ: SettingTypeBoolean, in: "yes", wantErr: ErrInvalidBoolean},

		{name: "select accepts option", typ: SettingTypeSelect, opts: selectOpts, in: "a", want: "a"},
		{name: "select rejects non-option", typ: SettingTypeSelect, opts: selectOpts, in: "z", wantErr: ErrInvalidOption},
		{name: "select unconstrained without options", typ: SettingTypeSelect, in: "z", want: "z"},

		{name: "string passthrough", typ: SettingTypeString, in: "hello", want: "hello"},
		{name: "nil always resets", typ: SettingTypeNumber, in: nil, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CoerceSettingValue(tc.typ, tc.opts, tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSetSystemSettingRejectsUnknownKey(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if err := db.SeedSystemSettings(ctx); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// System settings are declared by the server, so an unknown key is an error
	// rather than an implicit definition.
	_, err := db.SetSystemSetting(ctx, "NOT_A_REAL_SETTING", 1)
	if !errors.Is(err, ErrSettingUnknown) {
		t.Errorf("error = %v, want ErrSettingUnknown", err)
	}
}

func TestSetPluginSettingCreatesOnDemand(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Unlike system settings, a plugin setting is created with minimal metadata
	// if the plugin has not registered a definition yet.
	got, err := db.SetPluginSetting(ctx, "skier_aitagging", "server_url", "http://localhost:8000")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if got.Effective() != "http://localhost:8000" {
		t.Errorf("Effective() = %v", got.Effective())
	}
	if got.Type != SettingTypeString {
		t.Errorf("bare setting type = %q, want string", got.Type)
	}
}

func TestSettingNullStringsAreNormalised(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// "null" arriving from the frontend must not be stored as a literal string.
	got, err := db.SetPluginSetting(ctx, "p", "k", "null")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if got.Value.Valid && got.Value.Data != nil {
		t.Errorf("stored %#v, want SQL NULL", got.Value.Data)
	}
}

func TestRecommendationPreferences(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	if _, err := db.GetRecommendationPreference(ctx, "scenes"); !errors.Is(err, ErrNoPreference) {
		t.Fatalf("error = %v, want ErrNoPreference", err)
	}

	saved, err := db.SaveRecommendationPreference(ctx, "scenes", "personalized_tfidf",
		map[string]any{"min_watch_seconds": 30.0})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.RecommenderID != "personalized_tfidf" || saved.Config["min_watch_seconds"] != 30.0 {
		t.Errorf("saved wrong: %#v", saved)
	}

	// Saving again replaces rather than duplicating - context is unique.
	if _, err := db.SaveRecommendationPreference(ctx, "scenes", "segment_similarity", nil); err != nil {
		t.Fatalf("resave: %v", err)
	}
	got, err := db.GetRecommendationPreference(ctx, "scenes")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RecommenderID != "segment_similarity" {
		t.Errorf("recommender = %q, want segment_similarity", got.RecommenderID)
	}
	// A nil config is stored as an empty object, matching `config or {}`.
	if got.Config == nil || len(got.Config) != 0 {
		t.Errorf("config = %#v, want an empty map", got.Config)
	}

	all, err := db.ListRecommendationPreferences(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Errorf("listed %d preferences, want 1", len(all))
	}
}
