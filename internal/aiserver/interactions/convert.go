package interactions

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// Values arriving from the browser have been through JSON, and some have been
// through a string conversion on the way. These helpers accept what actually
// turns up rather than what the schema nominally promises.

// toFloat coerces a JSON-decoded value to a float.
func toFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	default:
		return 0, false
	}
}

// toInt coerces a JSON-decoded value to an int.
func toInt(v any) (int, bool) {
	switch t := v.(type) {
	case float64:
		return int(t), true
	case int:
		return t, true
	case int64:
		return int(t), true
	case json.Number:
		n, err := t.Int64()
		return int(n), err == nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	default:
		return 0, false
	}
}

// metaString reads a string metadata field.
func metaString(meta map[string]any, key string) (string, bool) {
	if meta == nil {
		return "", false
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return "", false
	}
	if s, isString := raw.(string); isString {
		return s, true
	}
	return "", false
}

// metaMap reads a nested object metadata field.
func metaMap(meta map[string]any, key string) (map[string]any, bool) {
	if meta == nil {
		return nil, false
	}
	raw, ok := meta[key]
	if !ok || raw == nil {
		return nil, false
	}
	m, isMap := raw.(map[string]any)
	return m, isMap
}

// parseEventTimestamp interprets a `last_entity.ts` value.
//
// The frontend has emitted this in more than one form over time, so all three
// are accepted in the same order the original tried them: ISO-8601 first, then
// epoch milliseconds for an all-digit string, and otherwise nothing - the
// caller falls back to the event's own timestamp.
func parseEventTimestamp(raw any) (time.Time, bool) {
	switch t := raw.(type) {
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return time.Time{}, false
		}
		for _, layout := range []string{
			time.RFC3339Nano,
			time.RFC3339,
			"2006-01-02T15:04:05.999999999",
			"2006-01-02 15:04:05.999999999",
			"2006-01-02T15:04:05",
		} {
			if parsed, err := time.Parse(layout, s); err == nil {
				return parsed.UTC(), true
			}
		}
		// All-digit strings are epoch milliseconds.
		if isAllDigits(s) {
			if ms, err := strconv.ParseInt(s, 10, 64); err == nil {
				return time.UnixMilli(ms).UTC(), true
			}
		}
		return time.Time{}, false

	case float64:
		return time.UnixMilli(int64(t)).UTC(), true
	case int64:
		return time.UnixMilli(t).UTC(), true
	case json.Number:
		if ms, err := t.Int64(); err == nil {
			return time.UnixMilli(ms).UTC(), true
		}
		return time.Time{}, false

	default:
		return time.Time{}, false
	}
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// pgIntMax is the largest value a Postgres `integer` column could hold.
//
// The original clamped entity ids to this range because its database used
// 32-bit integers. SQLite's INTEGER is 64-bit so the clamp is no longer
// technically required, but it is kept deliberately: removing it changes which
// out-of-range ids collapse into bucket 0, and therefore changes the derived
// counters for anyone whose UI ever emitted one.
const pgIntMax = 2147483647

// sanitizeEntityID coerces an entity id into the historical 32-bit range,
// mapping anything invalid or out of range to 0.
func sanitizeEntityID(raw any) int {
	v, ok := toInt(raw)
	if !ok {
		return 0
	}
	if v > pgIntMax || v < -pgIntMax {
		return 0
	}
	return v
}
