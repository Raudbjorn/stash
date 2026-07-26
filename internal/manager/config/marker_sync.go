package config

import "github.com/stashapp/stash/pkg/logger"

// Marker Sync default values. These are applied by GetMarkerSyncConfig when the
// corresponding field is unset (see the getter for the exact unset semantics).
const (
	markerSyncDefaultTolerance = 15.0
	markerSyncDefaultMode      = "skip"
)

// markerSyncDefaultSkipTags is the default set of primary tag names whose
// candidate markers are ignored during a sync. It mirrors the community
// timestamp.trade / TPDB skip conventions.
var markerSyncDefaultSkipTags = []string{
	"[Timestamp: Skip Sync]",
	"[TPDB: Skip Marker]",
}

// MarkerSyncSourceConfig configures a single marker sync source (fetch and, for
// bidirectional sources, submit). It is bound to both the GraphQL result and
// input types (see gqlgen.yml).
type MarkerSyncSourceConfig struct {
	// Enabled toggles the source on or off.
	Enabled bool `json:"enabled" koanf:"enabled"`
	// APIKey is the source bearer token (e.g. the TPDB API key). When empty the
	// builder falls back to the matching stash_boxes entry where applicable.
	APIKey string `json:"api_key" koanf:"api_key"`
	// BaseURL overrides the source's production base URL. Empty means production.
	BaseURL string `json:"base_url" koanf:"base_url"`
	// RequestsPerMinute optionally rate limits requests. <= 0 disables limiting.
	RequestsPerMinute int `json:"requests_per_minute" koanf:"requests_per_minute"`
}

// MarkerSyncConfig is the persisted configuration for the native marker sync
// feature. It is stored under the MarkerSync config key and bound to both the
// GraphQL result and input types (see gqlgen.yml).
type MarkerSyncConfig struct {
	// ToleranceSeconds is the matching window, in seconds, within which a
	// candidate marker is considered a duplicate of an existing one. Default 15.
	ToleranceSeconds float64 `json:"tolerance_seconds" koanf:"tolerance_seconds"`
	// Mode is the apply mode: "skip", "merge" or "overwrite". Default "skip".
	Mode string `json:"mode" koanf:"mode"`
	// SkipTags is the set of primary tag names whose candidates are ignored.
	SkipTags []string `json:"skip_tags" koanf:"skip_tags"`
	// TagAware, when true, requires a matching primary tag for a candidate to be
	// treated as a duplicate. Default true.
	TagAware bool `json:"tag_aware" koanf:"tag_aware"`
	// FireHooks, when true, fires marker create post-hooks for created markers.
	// Default false.
	FireHooks bool `json:"fire_hooks" koanf:"fire_hooks"`
	// SyncURLs, when true, merges source-provided extra scene URLs into the
	// scene. Default true.
	SyncURLs bool `json:"sync_urls" koanf:"sync_urls"`
	// SyncGalleries, when true, links source-provided galleries (matched by file
	// md5) to the scene. Default true.
	SyncGalleries bool `json:"sync_galleries" koanf:"sync_galleries"`
	// SyncGroups, when true, creates/links source-provided groups on the scene.
	// Default true.
	SyncGroups bool `json:"sync_groups" koanf:"sync_groups"`
	// FunscriptPath is the root directory recursively scanned for *.funscript
	// files by the funscript index task. Empty disables funscript indexing.
	FunscriptPath string `json:"funscript_path" koanf:"funscript_path"`
	// MatchFunscripts, when true, copies a source-provided funscript (matched by
	// md5 against the local index) next to the scene's video during a sync.
	// Default true.
	MatchFunscripts bool `json:"match_funscripts" koanf:"match_funscripts"`
	// SubmitFunscriptHash, when true, attaches the scene's indexed funscript
	// hashes to the submit payload. Default true.
	SubmitFunscriptHash bool `json:"submit_funscript_hash" koanf:"submit_funscript_hash"`
	// ThePornDB configures the ThePornDB (fetch-only) source.
	ThePornDB MarkerSyncSourceConfig `json:"theporndb" koanf:"theporndb"`
	// TimestampTrade configures the timestamp.trade (bidirectional) source.
	TimestampTrade MarkerSyncSourceConfig `json:"timestamp_trade" koanf:"timestamp_trade"`
}

// MarkerSyncSourceConfigInput is the partial-update input for a marker sync
// source. Every field is optional; only non-nil fields are applied on write, so
// a mutation that omits a field (for example theporndb.api_key) preserves the
// stored value instead of clobbering it. Bound to the GraphQL
// MarkerSyncSourceConfigInput (see gqlgen.yml).
type MarkerSyncSourceConfigInput struct {
	Enabled           *bool   `json:"enabled"`
	APIKey            *string `json:"api_key"`
	BaseURL           *string `json:"base_url"`
	RequestsPerMinute *int    `json:"requests_per_minute"`
}

// applyTo returns base with every provided (non-nil) field overlaid. A nil
// receiver leaves base unchanged (the source was omitted from the mutation).
func (in *MarkerSyncSourceConfigInput) applyTo(base MarkerSyncSourceConfig) MarkerSyncSourceConfig {
	if in == nil {
		return base
	}
	if in.Enabled != nil {
		base.Enabled = *in.Enabled
	}
	if in.APIKey != nil {
		base.APIKey = *in.APIKey
	}
	if in.BaseURL != nil {
		base.BaseURL = *in.BaseURL
	}
	if in.RequestsPerMinute != nil {
		base.RequestsPerMinute = *in.RequestsPerMinute
	}
	return base
}

// MarkerSyncConfigInput is the partial-update input for the marker sync
// configuration. Every field is optional; ApplyTo overlays only provided
// (non-nil) fields onto the current config, so a partial mutation never wipes
// unspecified fields (notably the per-source API keys). Bound to the GraphQL
// MarkerSyncConfigInput (see gqlgen.yml).
type MarkerSyncConfigInput struct {
	ToleranceSeconds    *float64                     `json:"tolerance_seconds"`
	Mode                *string                      `json:"mode"`
	SkipTags            []string                     `json:"skip_tags"`
	TagAware            *bool                        `json:"tag_aware"`
	FireHooks           *bool                        `json:"fire_hooks"`
	SyncURLs            *bool                        `json:"sync_urls"`
	SyncGalleries       *bool                        `json:"sync_galleries"`
	SyncGroups          *bool                        `json:"sync_groups"`
	FunscriptPath       *string                      `json:"funscript_path"`
	MatchFunscripts     *bool                        `json:"match_funscripts"`
	SubmitFunscriptHash *bool                        `json:"submit_funscript_hash"`
	ThePornDB           *MarkerSyncSourceConfigInput `json:"theporndb"`
	TimestampTrade      *MarkerSyncSourceConfigInput `json:"timestamp_trade"`
}

// ApplyTo returns base with every provided (non-nil) field overlaid. SkipTags is
// a slice, so nil means "not provided" (base kept) while a non-nil value
// (including an empty slice) replaces the stored tags.
func (in *MarkerSyncConfigInput) ApplyTo(base MarkerSyncConfig) MarkerSyncConfig {
	if in == nil {
		return base
	}
	if in.ToleranceSeconds != nil {
		base.ToleranceSeconds = *in.ToleranceSeconds
	}
	if in.Mode != nil {
		base.Mode = *in.Mode
	}
	if in.SkipTags != nil {
		base.SkipTags = append([]string(nil), in.SkipTags...)
	}
	if in.TagAware != nil {
		base.TagAware = *in.TagAware
	}
	if in.FireHooks != nil {
		base.FireHooks = *in.FireHooks
	}
	if in.SyncURLs != nil {
		base.SyncURLs = *in.SyncURLs
	}
	if in.SyncGalleries != nil {
		base.SyncGalleries = *in.SyncGalleries
	}
	if in.SyncGroups != nil {
		base.SyncGroups = *in.SyncGroups
	}
	if in.FunscriptPath != nil {
		base.FunscriptPath = *in.FunscriptPath
	}
	if in.MatchFunscripts != nil {
		base.MatchFunscripts = *in.MatchFunscripts
	}
	if in.SubmitFunscriptHash != nil {
		base.SubmitFunscriptHash = *in.SubmitFunscriptHash
	}
	base.ThePornDB = in.ThePornDB.applyTo(base.ThePornDB)
	base.TimestampTrade = in.TimestampTrade.applyTo(base.TimestampTrade)
	return base
}

// markerSyncDefaults returns a fully-populated MarkerSyncConfig representing the
// behaviour of an installation that has never configured marker sync.
func markerSyncDefaults() MarkerSyncConfig {
	return MarkerSyncConfig{
		ToleranceSeconds:    markerSyncDefaultTolerance,
		Mode:                markerSyncDefaultMode,
		SkipTags:            append([]string(nil), markerSyncDefaultSkipTags...),
		TagAware:            true,
		FireHooks:           false,
		SyncURLs:            true,
		SyncGalleries:       true,
		SyncGroups:          true,
		FunscriptPath:       "",
		MatchFunscripts:     true,
		SubmitFunscriptHash: true,
		ThePornDB:           MarkerSyncSourceConfig{Enabled: true},
		TimestampTrade:      MarkerSyncSourceConfig{Enabled: true},
	}
}

// GetMarkerSyncConfig returns the persisted marker sync configuration, applying
// defaults for unset fields. It mirrors GetStashBoxes but adds defaulting.
//
// When the MarkerSync key has never been set, a fully-defaulted config is
// returned (both sources enabled, TagAware true, the default skip tags). When
// the key is present, the stored values are respected verbatim except that
// zero-valued ToleranceSeconds, Mode and SkipTags are still defaulted - a bool
// (TagAware/Enabled/FireHooks) cannot be distinguished from unset once the key
// exists, so its stored value (including false) is honoured.
func (i *Config) GetMarkerSyncConfig() MarkerSyncConfig {
	// Detect whether the key was ever configured. A read lock is taken only for
	// the existence probe; unmarshalKey takes its own lock afterwards.
	i.RLock()
	set := i.forKey(MarkerSync).Exists(MarkerSync)
	i.RUnlock()

	if !set {
		return markerSyncDefaults()
	}

	var cfg MarkerSyncConfig
	if err := i.unmarshalKey(MarkerSync, &cfg); err != nil {
		logger.Warnf("error unmarshalling %s config: %v", MarkerSync, err)
	}

	// Zero-guard the numeric/string/slice fields that cannot be told apart from
	// a deliberate "unset within a set key".
	if cfg.ToleranceSeconds == 0 {
		cfg.ToleranceSeconds = markerSyncDefaultTolerance
	}
	if cfg.Mode == "" {
		cfg.Mode = markerSyncDefaultMode
	}
	if cfg.SkipTags == nil {
		cfg.SkipTags = append([]string(nil), markerSyncDefaultSkipTags...)
	}

	return cfg
}
