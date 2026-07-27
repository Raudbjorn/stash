package store

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"strings"
)

// JSON-typed columns are stored as plain TEXT and marshalled in Go.
//
// Nothing in the ported queries filters on JSON contents, so the engine's
// json_*() functions are deliberately unused: keeping the data opaque to SQL
// means one less pre-1.0 surface to depend on.

// JSONText carries a JSON value of type T through a TEXT column.
//
// A SQL NULL scans to Valid=false, which is distinct from a stored JSON `null`.
// The Python original leaned on that distinction (`JSON(none_as_null=True)`),
// so it is preserved here.
//
// The payload field is Data rather than Value because Value is taken by the
// driver.Valuer method.
type JSONText[T any] struct {
	Data  T
	Valid bool
}

// Scan implements sql.Scanner.
func (j *JSONText[T]) Scan(src any) error {
	var raw []byte
	switch v := src.(type) {
	case nil:
		var zero T
		j.Data, j.Valid = zero, false
		return nil
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("store: cannot scan %T into JSONText", src)
	}

	if len(raw) == 0 {
		var zero T
		j.Data, j.Valid = zero, false
		return nil
	}
	if err := json.Unmarshal(raw, &j.Data); err != nil {
		return fmt.Errorf("store: decode JSON column: %w", err)
	}
	j.Valid = true
	return nil
}

// Value implements driver.Valuer.
func (j JSONText[T]) Value() (driver.Value, error) {
	if !j.Valid {
		return nil, nil
	}
	raw, err := json.Marshal(j.Data)
	if err != nil {
		return nil, fmt.Errorf("store: encode JSON column: %w", err)
	}
	return string(raw), nil
}

// JSONOf wraps a value for writing.
func JSONOf[T any](v T) JSONText[T] { return JSONText[T]{Data: v, Valid: true} }

// NullJSON is an explicit SQL NULL for a JSON column.
func NullJSON[T any]() JSONText[T] { return JSONText[T]{} }

// MarshalArg renders an arbitrary value as a query argument for a JSON column,
// mapping nil to SQL NULL. Convenience for call sites that do not need the full
// JSONText round trip.
func MarshalArg(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("store: encode JSON argument: %w", err)
	}
	return string(raw), nil
}

// nullStringTokens are the placeholder values the Python server's
// normalize_null_strings treated as absent. They arrive from plugin manifests
// and frontend payloads where a missing value was stringified somewhere along
// the way.
var nullStringTokens = map[string]struct{}{
	"null": {}, "none": {}, "nil": {}, "undefined": {},
}

// NormalizeNullStrings recursively replaces placeholder null strings with nil,
// reproducing utils/string_utils.normalize_null_strings.
//
// It is applied to inbound event metadata and plugin manifest fields, where a
// literal "null" string would otherwise be stored and later compared as a real
// value.
func NormalizeNullStrings(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if _, isNull := nullStringTokens[strings.ToLower(strings.TrimSpace(t))]; isNull {
			return nil
		}
		return t
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = NormalizeNullStrings(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = NormalizeNullStrings(val)
		}
		return out
	default:
		return v
	}
}
