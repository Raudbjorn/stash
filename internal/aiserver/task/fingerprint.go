package task

import (
	"encoding/json"

	"github.com/stashapp/stash/internal/aiserver/action"
)

// Submission deduplication compares two canonical keys: one for the context,
// one for the parameters. Getting this wrong fails silently and expensively -
// duplicate submissions stop being caught, and users pay for the same AI run
// twice without anything appearing broken.
//
// The context key reproduces Pydantic's
// model_dump(by_alias=True, exclude_none=True, exclude_defaults=True):
//
//   - by_alias      -> camelCase keys, matching what the frontend sends.
//   - exclude_none  -> absent optional fields are omitted entirely.
//   - exclude_defaults -> a field equal to its declared default is ALSO omitted,
//     which is the subtle one: isDetailView is omitted when false, so a library
//     submission and a submission that merely spells out isDetailView:false
//     produce the same key.
//
// The nil-versus-empty-slice distinction matters and is preserved: an absent
// selectedIds is nil and omitted, while an explicit `[]` is not the default and
// is included as an empty array.
//
// The encoding is canonical (sorted keys, no whitespace) but not byte-identical
// to Python's json.dumps - Go renders 1.0 as "1" where Python writes "1.0".
// That is fine: both sides of every comparison are produced here, so only
// internal consistency matters.

// fingerprint returns the (context, params) key pair for a submission.
func fingerprint(ctx action.ContextInput, params map[string]any) (string, string, error) {
	ctxKey, err := canonicalJSON(contextFingerprintMap(ctx))
	if err != nil {
		return "", "", err
	}
	paramsKey, err := canonicalJSON(normalizeParams(params))
	if err != nil {
		return "", "", err
	}
	return ctxKey, paramsKey, nil
}

// contextFingerprintMap applies the exclude_none + exclude_defaults rules.
func contextFingerprintMap(ctx action.ContextInput) map[string]any {
	out := map[string]any{
		// page is required and has no default, so it is always present.
		"page": ctx.Page,
	}
	if ctx.EntityID != nil {
		out["entityId"] = *ctx.EntityID
	}
	if ctx.IsDetailView {
		// Default is false; only a true value survives exclude_defaults.
		out["isDetailView"] = true
	}
	if ctx.SelectedIDs != nil {
		out["selectedIds"] = ctx.SelectedIDs
	}
	if ctx.VisibleIDs != nil {
		out["visibleIds"] = ctx.VisibleIDs
	}
	return out
}

// normalizeParams makes a nil map encode as {} rather than null, so a task
// submitted without parameters matches one submitted with an empty object.
func normalizeParams(params map[string]any) map[string]any {
	if params == nil {
		return map[string]any{}
	}
	return params
}

// canonicalJSON encodes deterministically: encoding/json sorts map keys and
// emits no incidental whitespace, which is exactly the shape needed.
func canonicalJSON(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
