package pluginhost

import (
	"encoding/json"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/recommend"
)

// Decoding the host's registry snapshot.
//
// The snapshot arrives as loosely-typed maps because it originates in Python
// decorators, where a plugin author can write anything. Everything is decoded
// through JSON rather than read key by key: it keeps one definition of each
// wire shape - the Go struct tags - instead of a second, hand-written one that
// would drift from it.

// decodeAction converts a registry entry into an action definition.
func decodeAction(entry map[string]any) (action.Definition, string, bool) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return action.Definition{}, "", false
	}

	var def action.Definition
	if err := json.Unmarshal(raw, &def); err != nil || def.ID == "" {
		return action.Definition{}, "", false
	}

	// The owning plugin travels alongside the definition rather than inside it,
	// since it is host bookkeeping and not part of the UI contract.
	plugin, _ := entry["plugin"].(string)

	// An action with no service cannot be scheduled - the scheduler keys
	// concurrency on it - so the plugin name is the sensible default and
	// matches what a single-service plugin would have written anyway.
	if def.Service == "" {
		def.Service = plugin
	}
	return def, plugin, true
}

// decodeRecommender converts a registry entry into a recommender definition.
func decodeRecommender(entry map[string]any) (recommend.Definition, string, bool) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return recommend.Definition{}, "", false
	}

	var def recommend.Definition
	if err := json.Unmarshal(raw, &def); err != nil || def.ID == "" {
		return recommend.Definition{}, "", false
	}

	plugin, _ := entry["plugin"].(string)
	return def, plugin, true
}

// serviceSpec is a service as the host reports it.
type serviceSpec struct {
	Name           string `json:"name"`
	Plugin         string `json:"plugin"`
	MaxConcurrency int    `json:"max_concurrency"`
	ServerURL      string `json:"server_url"`
}

func decodeService(entry map[string]any) (serviceSpec, bool) {
	raw, err := json.Marshal(entry)
	if err != nil {
		return serviceSpec{}, false
	}

	var spec serviceSpec
	if err := json.Unmarshal(raw, &spec); err != nil || spec.Name == "" {
		return serviceSpec{}, false
	}
	if spec.MaxConcurrency < 1 {
		spec.MaxConcurrency = 1
	}
	return spec, true
}

// contextToMap renders a context for the wire.
//
// Marshalled through the struct so the camelCase names the frontend uses are
// the ones the plugin sees, which is what makes the compat shim's ctx object
// indistinguishable from the Python server's.
func contextToMap(in action.ContextInput) map[string]any {
	raw, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

// recommendResult decodes what a recommender returned.
func recommendResult(value any) (recommend.Result, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return recommend.Result{}, err
	}
	var result recommend.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		return recommend.Result{}, err
	}
	if result.Scenes == nil {
		result.Scenes = []recommend.SceneModel{}
	}
	return result, nil
}
