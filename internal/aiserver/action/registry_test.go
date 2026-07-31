package action

import (
	"context"
	"testing"
)

func noopHandler(context.Context, ContextInput, map[string]any, Handle) (any, error) {
	return nil, nil
}

func sp(s string) *string { return &s }

// The selection modes do not mean what their names suggest. The real axis is
// detail view versus library view; selection counts barely matter.
func TestContextRuleMatches(t *testing.T) {
	library := ContextInput{Page: "scenes"}
	librarySelected := ContextInput{Page: "scenes", SelectedIDs: []string{"1"}}
	libraryVisible := ContextInput{Page: "scenes", VisibleIDs: []string{"1"}}
	detail := ContextInput{Page: "scenes", IsDetailView: true, EntityID: sp("7")}

	cases := []struct {
		name string
		rule ContextRule
		ctx  ContextInput
		want bool
	}{
		{"single matches detail", ContextRule{Selection: SelectionSingle}, detail, true},
		{"single rejects library", ContextRule{Selection: SelectionSingle}, library, false},

		{"multi needs a selection", ContextRule{Selection: SelectionMulti}, librarySelected, true},
		{"multi rejects empty selection", ContextRule{Selection: SelectionMulti}, library, false},
		{"multi rejects detail", ContextRule{Selection: SelectionMulti}, detail, false},

		{"none requires empty selection", ContextRule{Selection: SelectionNone}, library, true},
		{"none rejects a selection", ContextRule{Selection: SelectionNone}, librarySelected, false},

		{"page needs visible ids", ContextRule{Selection: SelectionPage}, libraryVisible, true},
		{"page rejects without visible ids", ContextRule{Selection: SelectionPage}, library, false},
		{"page rejects a selection", ContextRule{Selection: SelectionPage}, librarySelected, false},

		{"all requires empty selection", ContextRule{Selection: SelectionAll}, library, true},
		{"all rejects a selection", ContextRule{Selection: SelectionAll}, librarySelected, false},

		{"both matches any library view", ContextRule{Selection: SelectionBoth}, library, true},
		{"both matches a selected library view", ContextRule{Selection: SelectionBoth}, librarySelected, true},
		{"both rejects detail", ContextRule{Selection: SelectionBoth}, detail, false},

		// An unset selection defaults to "both".
		{"empty selection defaults to both", ContextRule{}, library, true},
		{"empty selection rejects detail", ContextRule{}, detail, false},

		// Page filtering applies before anything else.
		{"page filter excludes", ContextRule{Pages: []string{"images"}}, library, false},
		{"page filter includes", ContextRule{Pages: []string{"scenes", "images"}}, library, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.rule.Matches(tc.ctx); got != tc.want {
				t.Errorf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDefinitionApplicability(t *testing.T) {
	library := ContextInput{Page: "scenes"}
	detail := ContextInput{Page: "scenes", IsDetailView: true}

	// No rules means applicable everywhere.
	unrestricted := Definition{ID: "a"}
	if !unrestricted.IsApplicable(library) || !unrestricted.IsApplicable(detail) {
		t.Error("an action with no rules should apply everywhere")
	}

	// Any matching rule is enough.
	either := Definition{ID: "b", Contexts: []ContextRule{
		{Selection: SelectionSingle},
		{Selection: SelectionMulti},
	}}
	if !either.IsApplicable(detail) {
		t.Error("should match via the single rule")
	}
	if either.IsApplicable(library) {
		t.Error("library with no selection should not match single or multi")
	}
}

// Resolve prefers the variant matching the current view, then falls back to the
// first applicable definition regardless of kind.
func TestResolvePrefersMatchingVariant(t *testing.T) {
	r := NewRegistry()

	detailDef := Definition{ID: "tag", Service: "svc", Label: "detail variant",
		Contexts: []ContextRule{{Selection: SelectionSingle}}}
	libraryDef := Definition{ID: "tag", Service: "svc", Label: "library variant",
		Contexts: []ContextRule{{Selection: SelectionMulti}}}

	// Register library first so a correct result cannot come from ordering.
	r.Register(libraryDef, noopHandler)
	r.Register(detailDef, noopHandler)

	got, ok := r.Resolve("tag", ContextInput{Page: "scenes", IsDetailView: true})
	if !ok {
		t.Fatal("no resolution for a detail view")
	}
	if got.Definition.Label != "detail variant" {
		t.Errorf("detail view resolved to %q", got.Definition.Label)
	}

	got, ok = r.Resolve("tag", ContextInput{Page: "scenes", SelectedIDs: []string{"1"}})
	if !ok {
		t.Fatal("no resolution for a library view")
	}
	if got.Definition.Label != "library variant" {
		t.Errorf("library view resolved to %q", got.Definition.Label)
	}
}

// When no variant matches the preferred kind, the first applicable one wins.
func TestResolveFallsBackToFirstApplicable(t *testing.T) {
	r := NewRegistry()

	// Only a library variant exists, but the request is a detail view. It is
	// not applicable, so nothing should resolve.
	r.Register(Definition{ID: "tag", Service: "svc",
		Contexts: []ContextRule{{Selection: SelectionMulti}}}, noopHandler)

	if _, ok := r.Resolve("tag", ContextInput{Page: "scenes", IsDetailView: true}); ok {
		t.Error("resolved an inapplicable definition")
	}

	// An unrestricted definition is applicable everywhere and is classified as
	// "library", so a detail request falls back to it.
	r2 := NewRegistry()
	r2.Register(Definition{ID: "tag", Service: "svc", Label: "generic"}, noopHandler)
	got, ok := r2.Resolve("tag", ContextInput{Page: "scenes", IsDetailView: true})
	if !ok || got.Definition.Label != "generic" {
		t.Errorf("fallback failed: %v %v", got.Definition.Label, ok)
	}
}

func TestResolveUnknownAction(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Resolve("nope", ContextInput{Page: "scenes"}); ok {
		t.Error("resolved an unregistered action")
	}
}

func TestAvailableFiltersAndPreservesOrder(t *testing.T) {
	r := NewRegistry()

	r.Register(Definition{ID: "first", Service: "svc",
		Contexts: []ContextRule{{Selection: SelectionSingle}}}, noopHandler)
	r.Register(Definition{ID: "second", Service: "svc"}, noopHandler)
	r.Register(Definition{ID: "third", Service: "svc",
		Contexts: []ContextRule{{Selection: SelectionMulti}}}, noopHandler)

	// "first" matches via single; "second" has no rules so it applies
	// everywhere; "third" is library-only and drops out.
	got := r.Available(ContextInput{Page: "scenes", IsDetailView: true})
	if len(got) != 2 || got[0].ID != "first" || got[1].ID != "second" {
		t.Errorf("detail view available = %v, want [first second]", ids(got))
	}

	got = r.Available(ContextInput{Page: "scenes", SelectedIDs: []string{"1"}})
	if len(got) != 2 || got[0].ID != "second" || got[1].ID != "third" {
		t.Errorf("library available = %v, want [second third] in registration order", ids(got))
	}

	// Must be an empty slice, not nil, so it serialises as [] rather than null.
	empty := r.Available(ContextInput{Page: "nothing-matches", IsDetailView: true})
	if empty == nil {
		t.Error("Available returned nil; the endpoint must emit []")
	}
}

func TestUnregisterService(t *testing.T) {
	r := NewRegistry()
	r.Register(Definition{ID: "a", Service: "keep"}, noopHandler)
	r.Register(Definition{ID: "b", Service: "drop"}, noopHandler)
	// Two services sharing one action id: only the dropped one should go.
	r.Register(Definition{ID: "a", Service: "drop"}, noopHandler)

	r.UnregisterService("drop")

	all := r.ListAll()
	if len(all) != 1 || all[0].Service != "keep" {
		t.Errorf("after unregister: %v", all)
	}
	if _, ok := r.Resolve("b", ContextInput{Page: "scenes"}); ok {
		t.Error("dropped action still resolves")
	}
	if got := r.IDs(); len(got) != 1 || got[0] != "a" {
		t.Errorf("ids = %v, want [a]", got)
	}
}

func ids(defs []Definition) []string {
	out := make([]string, len(defs))
	for i, d := range defs {
		out[i] = d.ID
	}
	return out
}
