package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func ptr[T any](v T) *T { return &v }

// TestMarkerSyncConfigInputApplyTo verifies that a partial input overlays only
// the provided (non-nil) fields and never clobbers unspecified stored values -
// notably the per-source API keys. This is the fix for the config
// partial-update footgun.
func TestMarkerSyncConfigInputApplyTo(t *testing.T) {
	base := MarkerSyncConfig{
		ToleranceSeconds: 15,
		Mode:             "skip",
		SkipTags:         []string{"[Keep]"},
		TagAware:         true,
		FireHooks:        false,
		ThePornDB:        MarkerSyncSourceConfig{Enabled: true, APIKey: "tpdb-secret", RequestsPerMinute: 20},
		TimestampTrade:   MarkerSyncSourceConfig{Enabled: true, APIKey: "tt-secret"},
	}

	// A mutation that only flips fire_hooks and toggles theporndb.enabled must
	// preserve both API keys and every other unspecified field.
	in := &MarkerSyncConfigInput{
		FireHooks: ptr(true),
		ThePornDB: &MarkerSyncSourceConfigInput{Enabled: ptr(false)},
	}

	got := in.ApplyTo(base)

	assert.True(t, got.FireHooks)                        // provided -> applied
	assert.False(t, got.ThePornDB.Enabled)               // nested provided -> applied
	assert.Equal(t, "tpdb-secret", got.ThePornDB.APIKey) // NOT clobbered
	assert.Equal(t, 20, got.ThePornDB.RequestsPerMinute) // NOT clobbered
	assert.Equal(t, "tt-secret", got.TimestampTrade.APIKey)
	assert.True(t, got.TimestampTrade.Enabled)
	assert.Equal(t, 15.0, got.ToleranceSeconds)
	assert.Equal(t, "skip", got.Mode)
	assert.True(t, got.TagAware)
	assert.Equal(t, []string{"[Keep]"}, got.SkipTags) // nil slice input -> kept

	// A non-nil (even empty) SkipTags replaces; a provided source field overrides.
	in2 := &MarkerSyncConfigInput{
		SkipTags:  []string{},
		Mode:      ptr("overwrite"),
		ThePornDB: &MarkerSyncSourceConfigInput{APIKey: ptr("rotated")},
	}
	got2 := in2.ApplyTo(base)
	assert.Empty(t, got2.SkipTags)
	assert.Equal(t, "overwrite", got2.Mode)
	assert.Equal(t, "rotated", got2.ThePornDB.APIKey) // provided -> overrides
	assert.True(t, got2.ThePornDB.Enabled)            // unspecified nested -> kept

	// A nil input is a no-op.
	assert.Equal(t, base, (*MarkerSyncConfigInput)(nil).ApplyTo(base))
}

// TestMarkerSyncConfigValidate covers the pre-persist validation of the merged
// config: bad mode, negative tolerance and negative per-source rate all fail;
// valid values (including a 0 tolerance meaning exact match) pass.
func TestMarkerSyncConfigValidate(t *testing.T) {
	valid := MarkerSyncConfig{Mode: "skip", ToleranceSeconds: 0}
	assert.NoError(t, valid.Validate())

	for _, m := range []string{"", "skip", "merge", "overwrite"} {
		assert.NoError(t, MarkerSyncConfig{Mode: m}.Validate(), "mode %q should be valid", m)
	}

	assert.Error(t, MarkerSyncConfig{Mode: "bogus"}.Validate())
	assert.Error(t, MarkerSyncConfig{Mode: "skip", ToleranceSeconds: -1}.Validate())
	assert.Error(t, MarkerSyncConfig{
		Mode:      "skip",
		ThePornDB: MarkerSyncSourceConfig{RequestsPerMinute: -5},
	}.Validate())
	assert.Error(t, MarkerSyncConfig{
		Mode:           "skip",
		TimestampTrade: MarkerSyncSourceConfig{RequestsPerMinute: -1},
	}.Validate())
}

// TestGetMarkerSyncConfigDefaults verifies that an installation that has never
// configured marker sync gets a fully-defaulted config.
func TestGetMarkerSyncConfigDefaults(t *testing.T) {
	i := InitializeEmpty()

	cfg := i.GetMarkerSyncConfig()

	assert.Equal(t, markerSyncDefaultTolerance, cfg.ToleranceSeconds)
	assert.Equal(t, markerSyncDefaultMode, cfg.Mode)
	assert.True(t, cfg.TagAware)
	assert.False(t, cfg.FireHooks)
	assert.True(t, cfg.ThePornDB.Enabled)
	assert.True(t, cfg.TimestampTrade.Enabled)
	assert.Equal(t, []string{
		"[Timestamp: Skip Sync]",
		"[TPDB: Skip Marker]",
	}, cfg.SkipTags)
}

// TestGetMarkerSyncConfigPartial verifies that when the key is set, stored
// values are respected verbatim (including a false bool and a 0 tolerance, which
// means exact match) while the zero-valued Mode/SkipTags fields are defaulted.
func TestGetMarkerSyncConfigPartial(t *testing.T) {
	i := InitializeEmpty()

	// Set a config with both bools false and the guarded fields left zero.
	i.SetInterface(MarkerSync, MarkerSyncConfig{
		TagAware:       false,
		FireHooks:      true,
		ThePornDB:      MarkerSyncSourceConfig{Enabled: false, APIKey: "secret"},
		TimestampTrade: MarkerSyncSourceConfig{Enabled: true, RequestsPerMinute: 30},
		// ToleranceSeconds, Mode, SkipTags deliberately left zero/nil.
	})

	cfg := i.GetMarkerSyncConfig()

	// Stored bools respected verbatim (not re-defaulted).
	assert.False(t, cfg.TagAware)
	assert.True(t, cfg.FireHooks)
	assert.False(t, cfg.ThePornDB.Enabled)
	assert.True(t, cfg.TimestampTrade.Enabled)

	// Stored non-zero source values round-trip.
	assert.Equal(t, "secret", cfg.ThePornDB.APIKey)
	assert.Equal(t, 30, cfg.TimestampTrade.RequestsPerMinute)

	// Zero-valued Mode/SkipTags are still defaulted, but a stored 0 tolerance is
	// honoured as an exact-match window (the config is stored fully-merged, so 0
	// is deliberate, not "unset").
	assert.Equal(t, 0.0, cfg.ToleranceSeconds)
	assert.Equal(t, markerSyncDefaultMode, cfg.Mode)
	assert.Equal(t, []string{
		"[Timestamp: Skip Sync]",
		"[TPDB: Skip Marker]",
	}, cfg.SkipTags)
}

// TestGetMarkerSyncConfigStoredValues verifies that explicitly stored non-zero
// guarded fields are respected instead of defaulted.
func TestGetMarkerSyncConfigStoredValues(t *testing.T) {
	i := InitializeEmpty()

	i.SetInterface(MarkerSync, MarkerSyncConfig{
		ToleranceSeconds: 5,
		Mode:             "overwrite",
		SkipTags:         []string{"[Only This]"},
		TagAware:         true,
		ThePornDB:        MarkerSyncSourceConfig{Enabled: true, BaseURL: "http://local"},
		TimestampTrade:   MarkerSyncSourceConfig{Enabled: false},
	})

	cfg := i.GetMarkerSyncConfig()

	assert.Equal(t, 5.0, cfg.ToleranceSeconds)
	assert.Equal(t, "overwrite", cfg.Mode)
	assert.Equal(t, []string{"[Only This]"}, cfg.SkipTags)
	assert.True(t, cfg.TagAware)
	assert.True(t, cfg.ThePornDB.Enabled)
	assert.Equal(t, "http://local", cfg.ThePornDB.BaseURL)
	assert.False(t, cfg.TimestampTrade.Enabled)
}
