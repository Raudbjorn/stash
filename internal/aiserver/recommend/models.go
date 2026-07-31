// Package recommend hosts the recommender registry and the scene hydration the
// recommenders return to the UI.
//
// Recommenders themselves are supplied by plugins; this package is the
// framework they register into and the contract their results must satisfy.
package recommend

// Context names where in the UI recommendations are being requested.
type Context string

const (
	// ContextGlobalFeed is the standalone recommendations page.
	ContextGlobalFeed Context = "global_feed"
	// ContextSimilarScene is the "similar scenes" tab on a scene page.
	ContextSimilarScene Context = "similar_scene"
	// ContextPruneCandidates suggests scenes to consider removing.
	ContextPruneCandidates Context = "prune_candidates"
)

// Valid reports whether the context is one the server recognises.
func (c Context) Valid() bool {
	switch c {
	case ContextGlobalFeed, ContextSimilarScene, ContextPruneCandidates:
		return true
	default:
		return false
	}
}

// Config field types the frontend knows how to render.
const (
	FieldNumber     = "number"
	FieldSlider     = "slider"
	FieldSelect     = "select"
	FieldBoolean    = "boolean"
	FieldText       = "text"
	FieldSearch     = "search"
	FieldTags       = "tags"
	FieldPerformers = "performers"
	FieldEnum       = "enum"
)

// ConfigField declares one tunable of a recommender.
//
// The frontend renders these directly, so the JSON names are a contract.
type ConfigField struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Type     string `json:"type"`
	Default  any    `json:"default"`
	Required bool   `json:"required"`

	Min  *float64 `json:"min"`
	Max  *float64 `json:"max"`
	Step *float64 `json:"step"`

	Options []map[string]any `json:"options"`
	Help    *string          `json:"help"`

	// Tag-selector capabilities, read by the frontend to shape its picker.
	TagCombination          *string  `json:"tag_combination"`
	ConstraintTypes         []string `json:"constraint_types"`
	AllowedCombinationModes []string `json:"allowed_combination_modes"`

	// Persist controls whether the field is saved with the user's preference.
	// Transient fields - a search box, say - set this false so a stale query is
	// not restored on the next visit.
	Persist bool `json:"persist"`
}

// Definition describes a recommender to the UI.
type Definition struct {
	ID          string        `json:"id"`
	Label       string        `json:"label"`
	Description string        `json:"description"`
	Contexts    []Context     `json:"contexts"`
	Config      []ConfigField `json:"config"`

	SupportsLimitOverride bool `json:"supports_limit_override"`
	SupportsPagination    bool `json:"supports_pagination"`
	NeedsSeedScenes       bool `json:"needs_seed_scenes"`
	AllowsMultiSeed       bool `json:"allows_multi_seed"`
	ExposesScores         bool `json:"exposes_scores"`
}

// AppliesTo reports whether the recommender is offered in a context.
func (d Definition) AppliesTo(ctx Context) bool {
	for _, c := range d.Contexts {
		if c == ctx {
			return true
		}
	}
	return false
}

// Request is what a recommender is asked to satisfy.
type Request struct {
	Context       Context        `json:"context"`
	RecommenderID string         `json:"recommenderId"`
	Config        map[string]any `json:"config"`
	SeedSceneIDs  []int          `json:"seedSceneIds"`
	Limit         *int           `json:"limit"`
	Offset        int            `json:"offset"`
}

// SceneModel is a hydrated scene as returned to the UI.
//
// Recommenders may attach their own fields (a score, an explanation); the core
// shape is fixed because the frontend renders scene cards from it.
type SceneModel struct {
	ID         int              `json:"id"`
	Title      *string          `json:"title"`
	Rating100  *int             `json:"rating100"`
	Studio     map[string]any   `json:"studio"`
	Paths      map[string]any   `json:"paths"`
	Performers []map[string]any `json:"performers"`
	Tags       []map[string]any `json:"tags"`
	Files      []map[string]any `json:"files"`

	Score     *float64       `json:"score,omitempty"`
	DebugMeta map[string]any `json:"debug_meta,omitempty"`
}

// Result is what a recommender returns.
//
// Total and HasMore are optional: a recommender that knows its full result set
// reports them and the server trusts it, otherwise the server paginates what it
// was given. See ApplyPagination.
type Result struct {
	Scenes  []SceneModel `json:"scenes"`
	Total   *int         `json:"total"`
	HasMore *bool        `json:"has_more"`
}
