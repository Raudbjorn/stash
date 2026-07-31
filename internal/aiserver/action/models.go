// Package action holds the context-aware action definitions the UI queries to
// decide which buttons to offer, and the registry services register them into.
package action

import "encoding/json"

// SelectionMode constrains which view an action applies to.
type SelectionMode string

const (
	SelectionSingle SelectionMode = "single"
	SelectionMulti  SelectionMode = "multi"
	SelectionBoth   SelectionMode = "both"
	SelectionNone   SelectionMode = "none"
	SelectionPage   SelectionMode = "page"
	SelectionAll    SelectionMode = "all"
)

// ContextInput describes where in the UI the user is and what they have
// selected.
//
// The JSON names are camelCase because that is what the shipped TypeScript
// sends (PageContext.ts). Note the asymmetry with task summaries, which are
// snake_case - both are load-bearing and must not be "tidied".
//
// SelectedIDs and VisibleIDs are slices rather than pointers on purpose: an
// absent field decodes to nil while `[]` decodes to an empty non-nil slice, and
// the fingerprint distinguishes the two exactly as Pydantic's exclude_none did.
type ContextInput struct {
	Page         string   `json:"page"`
	EntityID     *string  `json:"entityId,omitempty"`
	IsDetailView bool     `json:"isDetailView"`
	SelectedIDs  []string `json:"selectedIds,omitempty"`
	VisibleIDs   []string `json:"visibleIds,omitempty"`
}

// ContextFromMap decodes a context supplied as a loosely-typed map.
//
// Used where a context arrives from outside Go - a plugin submitting a child
// task, say. Routed through the struct tags rather than read key by key so
// there is exactly one definition of the wire shape.
func ContextFromMap(raw map[string]any) ContextInput {
	if len(raw) == 0 {
		return ContextInput{}
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return ContextInput{}
	}

	var in ContextInput
	if err := json.Unmarshal(encoded, &in); err != nil {
		return ContextInput{}
	}
	return in
}

// ContextRule is one applicability rule on an action.
type ContextRule struct {
	// Pages restricts the rule to specific page keys. Empty means any page.
	Pages []string `json:"pages,omitempty"`
	// Selection defaults to "both".
	Selection SelectionMode `json:"selection,omitempty"`
}

// Matches reports whether the rule applies to the given context.
//
// Ported verbatim from ContextRule.matches. The semantics are deliberately
// coarse and are not what the mode names suggest: selection counts are mostly
// ignored, and the real axis is detail view versus library view.
//   - "single" matches only a detail view.
//   - Every other mode matches only a library view.
//   - "both" is the legacy catch-all and matches any library view.
func (r ContextRule) Matches(ctx ContextInput) bool {
	if len(r.Pages) > 0 && !contains(r.Pages, ctx.Page) {
		return false
	}

	selectedCount := len(ctx.SelectedIDs)

	selection := r.Selection
	if selection == "" {
		selection = SelectionBoth
	}

	if selection == SelectionSingle {
		return ctx.IsDetailView
	}
	if ctx.IsDetailView {
		return false
	}

	switch selection {
	case SelectionMulti:
		return selectedCount > 0
	case SelectionNone:
		return selectedCount == 0
	case SelectionPage:
		return selectedCount == 0 && len(ctx.VisibleIDs) > 0
	case SelectionAll:
		return selectedCount == 0
	default: // "both"
		return true
	}
}

// ResultKind tells the frontend how to present an action's result.
type ResultKind string

const (
	ResultNone         ResultKind = "none"
	ResultDialog       ResultKind = "dialog"
	ResultNotification ResultKind = "notification"
	ResultStream       ResultKind = "stream"
)

// Definition is an action as advertised to the UI.
//
// Field names here are snake_case: FastAPI serialised this model by field name,
// and the TypeScript reads result_kind, dialog_type and input_schema directly.
type Definition struct {
	ID          string     `json:"id"`
	Label       string     `json:"label"`
	Description string     `json:"description"`
	Service     string     `json:"service"`
	ResultKind  ResultKind `json:"result_kind"`
	DialogType  *string    `json:"dialog_type"`

	Contexts    []ContextRule  `json:"contexts"`
	InputSchema map[string]any `json:"input_schema"`

	// DeduplicateSubmissions makes a second identical submission return 409
	// rather than queueing duplicate work. Defaults to true at registration.
	DeduplicateSubmissions bool `json:"deduplicate_submissions"`
}

// IsApplicable reports whether the action should be offered in this context.
// An action with no rules applies everywhere.
func (d Definition) IsApplicable(ctx ContextInput) bool {
	if len(d.Contexts) == 0 {
		return true
	}
	for _, rule := range d.Contexts {
		if rule.Matches(ctx) {
			return true
		}
	}
	return false
}

// kind classifies a definition as detail- or library-oriented, which drives
// Resolve's preference when several definitions share an id.
func (d Definition) kind() string {
	for _, rule := range d.Contexts {
		if rule.Selection == SelectionSingle {
			return "detail"
		}
	}
	return "library"
}

func contains(haystack []string, needle string) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}
