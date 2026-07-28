package recommend

import (
	"fmt"
	"sort"
	"strconv"
)

// Recommender configuration is validated leniently: an out-of-range value is
// clamped and a bad one falls back to its default, both with a warning, rather
// than failing the request. The user is tuning a feed, not submitting a form -
// returning something useful with a note beats returning an error.

// ValidateConfig applies defaults and constraints, returning the effective
// configuration and any warnings.
//
// The warning strings are part of the contract: the frontend displays them
// verbatim.
func ValidateConfig(def Definition, raw map[string]any) (map[string]any, []string) {
	if len(def.Config) == 0 {
		if raw == nil {
			return map[string]any{}, nil
		}
		return raw, nil
	}

	spec := make(map[string]ConfigField, len(def.Config))
	for _, f := range def.Config {
		spec[f.Name] = f
	}

	out := map[string]any{}
	var warnings []string

	// Declaration order, so warnings appear in the order the form is rendered.
	for _, field := range def.Config {
		value, present := raw[field.Name]
		if !present {
			value = field.Default
		}

		if field.Type == FieldNumber || field.Type == FieldSlider {
			if value != nil {
				f, ok := asFloat(value)
				if !ok {
					warnings = append(warnings, "config."+field.Name+" invalid numeric; using default")
					value = field.Default
				} else {
					if field.Min != nil && f < *field.Min {
						warnings = append(warnings, "config."+field.Name+" below min; clamped")
						f = *field.Min
					}
					if field.Max != nil && f > *field.Max {
						warnings = append(warnings, "config."+field.Name+" above max; clamped")
						f = *field.Max
					}
					value = f
				}
			}
		}

		// A missing required value is reported but not fatal: the recommender
		// may still produce something sensible.
		if field.Required && value == nil {
			warnings = append(warnings, "config."+field.Name+" required but missing")
		}

		out[field.Name] = value
	}

	// Undeclared keys are surfaced so a typo in a saved preference is visible
	// rather than silently ignored.
	var extras []string
	for key := range raw {
		if _, declared := spec[key]; !declared {
			extras = append(extras, key)
		}
	}
	sort.Strings(extras)
	for _, key := range extras {
		warnings = append(warnings, "config."+key+" ignored (undeclared)")
	}

	return out, warnings
}

// PersistableConfig strips fields marked non-persistent, so transient inputs
// such as a search box are not restored on the next visit.
func PersistableConfig(def Definition, config map[string]any) map[string]any {
	if len(def.Config) == 0 || len(config) == 0 {
		if config == nil {
			return map[string]any{}
		}
		return config
	}

	out := map[string]any{}
	for _, field := range def.Config {
		if !field.Persist {
			continue
		}
		if v, ok := config[field.Name]; ok {
			out[field.Name] = v
		}
	}
	return out
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(t, 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// Pagination is the page metadata returned alongside results.
type Pagination struct {
	Total      int  `json:"total"`
	Offset     int  `json:"offset"`
	Limit      int  `json:"limit"`
	NextOffset *int `json:"nextOffset"`
	HasMore    bool `json:"hasMore"`
}

// ApplyPagination reconciles what a recommender returned with what was asked
// for.
//
// Two modes, and which applies is decided by the recommender:
//   - It reported Total or HasMore, meaning it paginated internally: its page
//     is returned as-is and its counts are trusted.
//   - It reported neither, meaning it returned everything it had: the server
//     slices the requested window out of it.
//
// Total is floored at offset+len(page) either way, so a recommender that
// under-reports cannot produce a total that contradicts the page it just
// returned.
func ApplyPagination(result Result, offset int, limit *int) ([]SceneModel, Pagination) {
	if offset < 0 {
		offset = 0
	}

	scenes := result.Scenes
	if scenes == nil {
		scenes = []SceneModel{}
	}

	effectiveLimit := len(scenes)
	if limit != nil && *limit > 0 {
		effectiveLimit = *limit
	}

	if result.Total != nil || result.HasMore != nil {
		hasMore := false
		switch {
		case result.HasMore != nil:
			hasMore = *result.HasMore
		case result.Total != nil:
			hasMore = offset+len(scenes) < *result.Total
		}

		floor := offset + len(scenes)
		total := floor
		if result.Total != nil {
			total = *result.Total
		} else if hasMore {
			total = floor + 1
		}
		if total < floor {
			total = floor
		}

		var next *int
		if hasMore {
			n := offset + len(scenes)
			next = &n
		}

		return scenes, Pagination{
			Total: total, Offset: offset, Limit: effectiveLimit,
			NextOffset: next, HasMore: hasMore,
		}
	}

	// The recommender returned everything; slice here.
	total := len(scenes)
	start := min(offset, total)
	end := total
	if limit != nil && *limit > 0 {
		end = min(start+*limit, total)
	}
	page := scenes[start:end]

	hasMore := end < total
	var next *int
	if hasMore {
		next = &end
	}

	return page, Pagination{
		Total: total, Offset: offset, Limit: effectiveLimit,
		NextOffset: next, HasMore: hasMore,
	}
}

// describeContext renders a context for an error message.
func describeContext(c Context) string {
	return fmt.Sprintf("%q", string(c))
}
