package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Settings live in one table for both plugins and the server itself; system
// settings are simply rows under a reserved plugin name. The Python server did
// the same so that one API and one UI renderer covers both, and the frontend
// still depends on that shape.
const SystemPluginName = "__system__"

// Setting types understood by the frontend's input renderer.
const (
	SettingTypeString  = "string"
	SettingTypeNumber  = "number"
	SettingTypeBoolean = "boolean"
	SettingTypeSelect  = "select"
	SettingTypePathMap = "path_map"
)

// Validation failures carry the exact codes the existing TypeScript parses out
// of the `detail` field, so the frontend needs no change.
var (
	ErrInvalidNumber  = errors.New("INVALID_NUMBER")
	ErrInvalidBoolean = errors.New("INVALID_BOOLEAN")
	ErrInvalidOption  = errors.New("INVALID_OPTION")
	ErrSettingUnknown = errors.New("NOT_FOUND")
)

// Setting is one stored setting: its definition and its current value.
type Setting struct {
	ID          int64
	PluginName  string
	Key         string
	Type        string
	Label       *string
	Description *string
	Default     JSONText[any]
	Options     JSONText[[]any]
	Value       JSONText[any]
}

// Effective is the value in force: the explicit value when set, else the
// default. A null value means "reset to default", which is why this is not
// simply Value.
func (s Setting) Effective() any {
	if s.Value.Valid {
		return s.Value.Data
	}
	if s.Default.Valid {
		return s.Default.Data
	}
	return nil
}

// SettingDef declares a setting so it can be seeded.
type SettingDef struct {
	Key         string
	Type        string
	Label       string
	Description string
	Default     any
	Options     []any
}

// SystemSettingDefs are the settings that still mean something once the server
// runs inside Stash.
//
// Deliberately absent, because in-process they have no referent:
//   - STASH_URL, STASH_API_KEY: there is no remote Stash to address.
//   - STASH_DB_PATH: the repository is the database.
//   - PATH_MAPPINGS: file paths are already correct, with no container boundary.
//   - UI_SHARED_API_KEY: Stash's own session auth covers /api/v1.
//
// TASK_LOOP_INTERVAL and TASK_DEBUG moved to Stash's config file instead: they
// are infrastructure, and the scheduler needs them before a database exists.
var SystemSettingDefs = []SettingDef{
	{
		Key: "INTERACTION_MIN_SESSION_MINUTES", Type: SettingTypeNumber,
		Label:       "Interaction Min Session (min)",
		Description: "Minimum session duration in minutes for determining a derived o_count",
		Default:     10.0,
	},
	{
		Key: "INTERACTION_MERGE_TTL_SECONDS", Type: SettingTypeNumber,
		Label:       "Interaction Merge sessions TTL (s)",
		Description: "Time to merge sessions together if they occur less than this many seconds apart",
		Default:     120.0,
	},
	{
		Key: "SEGMENT_MERGE_GAP_SECONDS", Type: SettingTypeNumber,
		Label:       "Segment Merge Gap (s)",
		Description: "Merges interaction watch segments within this many seconds",
		Default:     0.5,
	},
	{
		Key: "INTERACTION_SEGMENT_TIME_MARGIN_SECONDS", Type: SettingTypeNumber,
		Label:       "Interaction Segment Time Margin (s)",
		Description: "Widens the event replay window by this many seconds at each end",
		Default:     2.0,
	},
	{
		// The Python server read this on both segment paths but never declared
		// it, so it always resolved to the caller's fallback of 1.5 and could
		// not be changed from the UI. Declaring it makes the knob real; the
		// default matches the old hard-coded fallback exactly.
		Key: "SEGMENT_MIN_DURATION_SECONDS", Type: SettingTypeNumber,
		Label:       "Segment Min Duration (s)",
		Description: "Watch segments shorter than this are discarded",
		Default:     1.5,
	},
}

// SeedSystemSettings inserts any missing system setting definitions, leaving
// existing rows (and therefore user-set values) untouched.
func (db *DB) SeedSystemSettings(ctx context.Context) error {
	return db.SeedSettings(ctx, SystemPluginName, SystemSettingDefs)
}

// SeedSettings inserts missing definitions for a plugin. Rows are inserted in
// declaration order because the API returns settings ordered by id and the
// frontend renders them in that order.
func (db *DB) SeedSettings(ctx context.Context, pluginName string, defs []SettingDef) error {
	return db.InTx(ctx, func(tx *sql.Tx) error {
		for _, def := range defs {
			var exists int
			err := tx.QueryRowContext(ctx,
				`SELECT 1 FROM plugin_settings WHERE plugin_name = ? AND key = ?`,
				pluginName, def.Key).Scan(&exists)
			if err == nil {
				continue // already defined
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("look up setting %s/%s: %w", pluginName, def.Key, err)
			}

			defaultArg, err := MarshalArg(def.Default)
			if err != nil {
				return fmt.Errorf("setting %s/%s default: %w", pluginName, def.Key, err)
			}
			var optionsArg any
			if def.Options != nil {
				if optionsArg, err = MarshalArg(def.Options); err != nil {
					return fmt.Errorf("setting %s/%s options: %w", pluginName, def.Key, err)
				}
			}

			now := NowMillis()
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO plugin_settings
				 (plugin_name, key, type, label, description, default_value, options, value, updated_at, created_at)
				 VALUES (?,?,?,?,?,?,?,NULL,?,?)`,
				pluginName, def.Key, def.Type,
				nullIfEmpty(def.Label), nullIfEmpty(def.Description),
				defaultArg, optionsArg, now, now,
			); err != nil {
				return fmt.Errorf("seed setting %s/%s: %w", pluginName, def.Key, err)
			}
		}
		return nil
	})
}

// ListSettings returns a plugin's settings in insertion order.
func (db *DB) ListSettings(ctx context.Context, pluginName string) ([]Setting, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT id, plugin_name, key, type, label, description, default_value, options, value
		 FROM plugin_settings WHERE plugin_name = ? ORDER BY id`, pluginName)
	if err != nil {
		return nil, fmt.Errorf("list settings for %s: %w", pluginName, err)
	}
	defer rows.Close()

	var out []Setting
	for rows.Next() {
		var s Setting
		var label, description sql.NullString
		if err := rows.Scan(&s.ID, &s.PluginName, &s.Key, &s.Type,
			&label, &description, &s.Default, &s.Options, &s.Value); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		s.Label = StringPtr(label)
		s.Description = StringPtr(description)
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settings: %w", err)
	}
	return out, nil
}

// GetSetting fetches one setting, returning ErrSettingUnknown when absent.
func (db *DB) GetSetting(ctx context.Context, pluginName, key string) (Setting, error) {
	var s Setting
	var label, description sql.NullString
	err := db.sql.QueryRowContext(ctx,
		`SELECT id, plugin_name, key, type, label, description, default_value, options, value
		 FROM plugin_settings WHERE plugin_name = ? AND key = ?`, pluginName, key).
		Scan(&s.ID, &s.PluginName, &s.Key, &s.Type,
			&label, &description, &s.Default, &s.Options, &s.Value)
	if errors.Is(err, sql.ErrNoRows) {
		return Setting{}, ErrSettingUnknown
	}
	if err != nil {
		return Setting{}, fmt.Errorf("get setting %s/%s: %w", pluginName, key, err)
	}
	s.Label = StringPtr(label)
	s.Description = StringPtr(description)
	return s, nil
}

// SetPluginSetting validates and stores a plugin setting, creating the row with
// minimal metadata if the plugin has not registered a definition yet. A nil
// value resets the setting to its default.
func (db *DB) SetPluginSetting(ctx context.Context, pluginName, key string, value any) (Setting, error) {
	existing, err := db.GetSetting(ctx, pluginName, key)
	switch {
	case errors.Is(err, ErrSettingUnknown):
		if err := db.createBareSetting(ctx, pluginName, key); err != nil {
			return Setting{}, err
		}
		if existing, err = db.GetSetting(ctx, pluginName, key); err != nil {
			return Setting{}, err
		}
	case err != nil:
		return Setting{}, err
	}
	return db.storeSettingValue(ctx, existing, value)
}

// SetSystemSetting validates and stores a system setting. Unlike plugin
// settings, an unknown key is an error rather than an implicit definition -
// system settings are declared by the server, not discovered.
func (db *DB) SetSystemSetting(ctx context.Context, key string, value any) (Setting, error) {
	existing, err := db.GetSetting(ctx, SystemPluginName, key)
	if err != nil {
		return Setting{}, err
	}
	return db.storeSettingValue(ctx, existing, value)
}

func (db *DB) createBareSetting(ctx context.Context, pluginName, key string) error {
	now := NowMillis()
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO plugin_settings
		 (plugin_name, key, type, label, description, default_value, options, value, updated_at, created_at)
		 VALUES (?,?,?,?,NULL,NULL,NULL,NULL,?,?)`,
		pluginName, key, SettingTypeString, key, now, now)
	if err != nil {
		return fmt.Errorf("create setting %s/%s: %w", pluginName, key, err)
	}
	return nil
}

func (db *DB) storeSettingValue(ctx context.Context, s Setting, value any) (Setting, error) {
	// Placeholder null strings arrive from the frontend and plugin manifests;
	// normalising here keeps a literal "null" from being stored and later
	// compared as a real value.
	value = NormalizeNullStrings(value)

	coerced, err := CoerceSettingValue(s.Type, s.Options, value)
	if err != nil {
		return Setting{}, err
	}

	arg, err := MarshalArg(coerced)
	if err != nil {
		return Setting{}, fmt.Errorf("encode setting %s/%s: %w", s.PluginName, s.Key, err)
	}
	if _, err := db.sql.ExecContext(ctx,
		`UPDATE plugin_settings SET value = ?, updated_at = ? WHERE id = ?`,
		arg, NowMillis(), s.ID); err != nil {
		return Setting{}, fmt.Errorf("update setting %s/%s: %w", s.PluginName, s.Key, err)
	}

	return db.GetSetting(ctx, s.PluginName, s.Key)
}

// CoerceSettingValue applies the declared type, mirroring the Python API's
// validation exactly so error codes and accepted inputs stay identical.
//
// A nil value is always allowed: it means "reset to default".
func CoerceSettingValue(settingType string, options JSONText[[]any], value any) (any, error) {
	if value == nil {
		return nil, nil
	}

	switch settingType {
	case SettingTypeNumber:
		f, ok := toFloat(value)
		if !ok {
			return nil, ErrInvalidNumber
		}
		return f, nil

	case SettingTypeBoolean:
		b, ok := toBool(value)
		if !ok {
			return nil, ErrInvalidBoolean
		}
		return b, nil

	case SettingTypeSelect:
		if !options.Valid || len(options.Data) == 0 {
			return value, nil // no declared options means no constraint
		}
		for _, opt := range options.Data {
			if opt == value {
				return value, nil
			}
		}
		return nil, ErrInvalidOption

	default:
		return value, nil
	}
}

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
	case bool:
		// Python's float(True) is 1.0; the original would have accepted this.
		if t {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

func toBool(v any) (bool, bool) {
	switch t := v.(type) {
	case bool:
		return t, true
	case float64:
		return t != 0, true
	case int:
		return t != 0, true
	case int64:
		return t != 0, true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		return false, false
	default:
		return false, false
	}
}

// SettingFloat reads a numeric system setting, falling back to def when the
// setting is missing or not a number. Hot-path callers should cache the result.
func (db *DB) SettingFloat(ctx context.Context, key string, def float64) float64 {
	s, err := db.GetSetting(ctx, SystemPluginName, key)
	if err != nil {
		return def
	}
	if f, ok := toFloat(s.Effective()); ok {
		return f
	}
	return def
}

// SettingBool reads a boolean system setting, falling back to def.
func (db *DB) SettingBool(ctx context.Context, key string, def bool) bool {
	s, err := db.GetSetting(ctx, SystemPluginName, key)
	if err != nil {
		return def
	}
	if b, ok := toBool(s.Effective()); ok {
		return b
	}
	return def
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
