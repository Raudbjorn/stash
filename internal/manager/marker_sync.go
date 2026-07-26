package manager

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/markersync"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/plugin/hook"
	"github.com/stashapp/stash/pkg/scene"
	"github.com/stashapp/stash/pkg/sliceutil"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

// MarkerSyncSelectMode selects which scenes a marker sync run operates on.
type MarkerSyncSelectMode string

const (
	// MarkerSyncSelectModeAll syncs every scene in the library.
	MarkerSyncSelectModeAll MarkerSyncSelectMode = "ALL"
	// MarkerSyncSelectModeIds syncs only the scenes named in SceneIDs.
	MarkerSyncSelectModeIds MarkerSyncSelectMode = "IDS"
	// MarkerSyncSelectModeOnlyWithoutMarkers syncs only scenes that currently
	// have no markers.
	MarkerSyncSelectModeOnlyWithoutMarkers MarkerSyncSelectMode = "ONLY_WITHOUT_MARKERS"
)

// AllMarkerSyncSelectMode enumerates every valid MarkerSyncSelectMode.
var AllMarkerSyncSelectMode = []MarkerSyncSelectMode{
	MarkerSyncSelectModeAll,
	MarkerSyncSelectModeIds,
	MarkerSyncSelectModeOnlyWithoutMarkers,
}

// IsValid reports whether e is a recognised select mode.
func (e MarkerSyncSelectMode) IsValid() bool {
	switch e {
	case MarkerSyncSelectModeAll, MarkerSyncSelectModeIds, MarkerSyncSelectModeOnlyWithoutMarkers:
		return true
	}
	return false
}

func (e MarkerSyncSelectMode) String() string {
	return string(e)
}

// UnmarshalGQL implements the graphql.Unmarshaler interface.
func (e *MarkerSyncSelectMode) UnmarshalGQL(v interface{}) error {
	str, ok := v.(string)
	if !ok {
		return fmt.Errorf("enums must be strings")
	}

	*e = MarkerSyncSelectMode(str)
	if !e.IsValid() {
		return fmt.Errorf("%s is not a valid MarkerSyncSelectMode", str)
	}
	return nil
}

// MarshalGQL implements the graphql.Marshaler interface.
func (e MarkerSyncSelectMode) MarshalGQL(w io.Writer) {
	fmt.Fprint(w, strconv.Quote(e.String()))
}

// MarkerSyncInput is the GraphQL input for the markerSync mutation. It is
// autobound by gqlgen (see gqlgen.yml).
type MarkerSyncInput struct {
	// SceneIDs restricts the sync to the given scene ids. It is required when
	// Mode is IDS and ignored otherwise.
	SceneIDs []string `json:"scene_ids"`
	// Mode selects the scene set to sync. When nil it defaults to IDS if
	// SceneIDs is set, otherwise ALL.
	Mode *MarkerSyncSelectMode `json:"mode"`
}

// resolveMode returns the effective select mode, applying the default rules.
func (i MarkerSyncInput) resolveMode() MarkerSyncSelectMode {
	if i.Mode != nil && i.Mode.IsValid() {
		return *i.Mode
	}
	if len(i.SceneIDs) > 0 {
		return MarkerSyncSelectModeIds
	}
	return MarkerSyncSelectModeAll
}

// tagResolver is the repository-backed markersync.TagResolver. It resolves a tag
// name to an id via FindByName -> FindByAlias -> Create. It is bound to the
// transaction carried by the context passed to ResolveOrCreate.
type tagResolver struct {
	r models.TagReaderWriter
}

// ResolveOrCreate implements markersync.TagResolver.
func (t *tagResolver) ResolveOrCreate(ctx context.Context, name string) (int, error) {
	const nocase = true

	existing, err := t.r.FindByName(ctx, name, nocase)
	if err != nil {
		return 0, fmt.Errorf("finding tag by name %q: %w", name, err)
	}
	if existing != nil {
		return existing.ID, nil
	}

	byAlias, err := t.r.FindByAlias(ctx, name, nocase)
	if err != nil {
		return 0, fmt.Errorf("finding tag by alias %q: %w", name, err)
	}
	if byAlias != nil {
		return byAlias.ID, nil
	}

	newTag := models.NewTag()
	newTag.Name = name
	if err := t.r.Create(ctx, &models.CreateTagInput{Tag: &newTag}); err != nil {
		return 0, fmt.Errorf("creating tag %q: %w", name, err)
	}
	return newTag.ID, nil
}

var _ markersync.TagResolver = (*tagResolver)(nil)

// markerWriter is the repository-backed markersync.MarkerWriter. It is bound to
// the transaction carried by the context passed to its methods.
type markerWriter struct {
	r models.Repository
}

// ExistingForScene implements markersync.MarkerWriter.
func (w *markerWriter) ExistingForScene(ctx context.Context, sceneID int) ([]markersync.ExistingMarker, error) {
	markers, err := w.r.SceneMarker.FindBySceneID(ctx, sceneID)
	if err != nil {
		return nil, fmt.Errorf("finding markers for scene %d: %w", sceneID, err)
	}

	existing := make([]markersync.ExistingMarker, 0, len(markers))
	for _, m := range markers {
		existing = append(existing, markersync.ExistingMarker{
			ID:           m.ID,
			Seconds:      m.Seconds,
			PrimaryTagID: m.PrimaryTagID,
		})
	}
	return existing, nil
}

// CreateMarker implements markersync.MarkerWriter. It mirrors the canonical
// SceneMarkerCreate mutation body.
func (w *markerWriter) CreateMarker(ctx context.Context, sceneID int, title string, seconds float64, endSeconds *float64, primaryTagID int, extraTagIDs []int) (int, error) {
	m := models.NewSceneMarker()
	m.Title = title
	m.Seconds = seconds
	m.PrimaryTagID = primaryTagID
	m.SceneID = sceneID

	// Only set the end time when it is valid (>= start); silently drop an
	// invalid end rather than failing the whole run.
	if endSeconds != nil && *endSeconds >= seconds {
		m.EndSeconds = endSeconds
	}

	qb := w.r.SceneMarker
	if err := qb.Create(ctx, &m); err != nil {
		return 0, fmt.Errorf("creating marker %q: %w", title, err)
	}

	// Never attach the primary tag as an extra tag.
	extra := sliceutil.Exclude(extraTagIDs, []int{m.PrimaryTagID})
	if err := qb.UpdateTags(ctx, m.ID, extra); err != nil {
		return 0, fmt.Errorf("setting extra tags on marker %d: %w", m.ID, err)
	}

	return m.ID, nil
}

// DeleteMarker implements markersync.MarkerWriter.
func (w *markerWriter) DeleteMarker(ctx context.Context, id int) error {
	if err := w.r.SceneMarker.Destroy(ctx, id); err != nil {
		return fmt.Errorf("destroying marker %d: %w", id, err)
	}
	return nil
}

var _ markersync.MarkerWriter = (*markerWriter)(nil)

// buildMarkerSyncSources constructs the marker sync sources from the persisted
// marker sync configuration. A source is only constructed when enabled in the
// config. The ThePornDB api key falls back to the matching stash-boxes entry
// when not set explicitly in the marker sync config.
func buildMarkerSyncSources() []markersync.Source {
	cfg := config.GetInstance().GetMarkerSyncConfig()

	var sources []markersync.Source

	// ThePornDB: only when enabled. Prefer the marker-sync api key; fall back to
	// the configured stash-boxes entry matching the ThePornDB endpoint. The
	// source self-disables (with a log line) when no key can be resolved.
	if cfg.ThePornDB.Enabled {
		tpdbKey := cfg.ThePornDB.APIKey
		if tpdbKey == "" {
			for _, box := range config.GetInstance().GetStashBoxes() {
				if box != nil && box.Endpoint == markersync.ThePornDBEndpoint {
					tpdbKey = box.APIKey
					break
				}
			}
		}

		tpdb := markersync.NewThePornDBSource(markersync.ThePornDBOptions{
			APIKey:            tpdbKey,
			Enabled:           tpdbKey != "",
			BaseURL:           cfg.ThePornDB.BaseURL,
			RequestsPerMinute: cfg.ThePornDB.RequestsPerMinute,
		})
		if reason := tpdb.DisabledReason(); reason != "" {
			logger.Infof("Marker Sync: %s", reason)
		}
		sources = append(sources, tpdb)
	}

	// timestamp.trade: only when enabled. No authentication required.
	if cfg.TimestampTrade.Enabled {
		sources = append(sources, markersync.NewTimestampTradeSource(markersync.TimestampTradeOptions{
			Enabled:           true,
			BaseURL:           cfg.TimestampTrade.BaseURL,
			RequestsPerMinute: cfg.TimestampTrade.RequestsPerMinute,
		}))
	}

	return sources
}

// markerSyncApplyOptions builds the apply options from the marker sync config.
// ToleranceSeconds is passed straight through: an unset key already yields the
// default (15) via GetMarkerSyncConfig, and a stored 0 is a deliberate
// exact-match request that must reach the apply engine unchanged.
func markerSyncApplyOptions(cfg config.MarkerSyncConfig) markersync.ApplyOptions {
	mode := cfg.Mode
	if mode == "" {
		mode = markersync.ModeSkip
	}
	return markersync.ApplyOptions{
		Tolerance: cfg.ToleranceSeconds,
		Mode:      mode,
		SkipTags:  cfg.SkipTags,
		TagAware:  cfg.TagAware,
	}
}

// markerSyncTask synchronises markers for a single scene.
type markerSyncTask struct {
	scene     *models.Scene
	sources   []markersync.Source
	opts      markersync.ApplyOptions
	fireHooks bool

	// extras toggles (Stage 5): merge extra scene URLs, link galleries,
	// create/link groups, copy matched funscripts. Each is gated independently and
	// only acts on sources that implement the matching capability-provider
	// interface.
	syncURLs        bool
	syncGalleries   bool
	syncGroups      bool
	matchFunscripts bool
}

// ttSkipSyncTagName is the tag whose presence on a gallery excludes it from
// marker-sync gallery linking, mirroring the community plugin's skip_sync_tag.
const ttSkipSyncTagName = "[Timestamp: Skip Sync]"

// GetDescription implements Task.
func (t *markerSyncTask) GetDescription() string {
	return fmt.Sprintf("Syncing markers for scene %d", t.scene.ID)
}

// Start implements Task. Fetching happens outside any transaction; the apply
// runs inside a single write transaction; post-hooks fire after the commit.
func (t *markerSyncTask) Start(ctx context.Context) {
	// 1. Build the scene identity from the (already loaded) stash ids.
	id := markersync.SceneIdentity{
		StashIDs: t.scene.StashIDs.List(),
	}

	// 2. Fetch candidates OUTSIDE of any transaction. Source order is priority;
	// Apply de-duplicates so a plain concat across sources is sufficient.
	var candidates []markersync.MarkerCandidate
	for _, src := range t.sources {
		if !src.Enabled() {
			continue
		}
		cands, err := src.FetchMarkers(ctx, id)
		if err != nil {
			logger.Warnf("Marker Sync: scene %d: source %s: %v", t.scene.ID, src.Name(), err)
			continue
		}
		candidates = append(candidates, cands...)
	}

	// 3. Apply markers INSIDE a write transaction (skipped when no candidates).
	if len(candidates) > 0 {
		var result markersync.ApplyResult
		r := instance.Repository
		if err := r.WithTxn(ctx, func(ctx context.Context) error {
			w := &markerWriter{r: r}
			tags := markersync.NewCachingTagResolver(&tagResolver{r: r.Tag})

			var err error
			result, err = markersync.Apply(ctx, w, tags, t.scene.ID, candidates, t.opts)
			return err
		}); err != nil {
			logger.Errorf("Marker Sync: scene %d: applying markers: %v", t.scene.ID, err)
		} else {
			// 4. Fire post-hooks AFTER the transaction commits, never inside it.
			if t.fireHooks {
				for _, markerID := range result.CreatedIDs {
					instance.PluginCache.ExecutePostHooks(ctx, markerID, hook.SceneMarkerCreatePost, nil, nil)
				}
			}

			logger.Infof("Marker Sync: scene %d: created %d, skipped %d, overwritten %d",
				t.scene.ID, result.Created, result.Skipped, result.Overwritten)
		}
	} else {
		logger.Debugf("Marker Sync: scene %d: no candidate markers from any source", t.scene.ID)
	}

	// 5. Apply extras (URLs / galleries / groups) for capable sources.
	t.applyExtras(ctx, id)
}

// applyExtras fetches and applies the extras (extra scene URLs, galleries,
// groups) for every source that implements the matching capability-provider
// interface, gated by the per-class config toggles. Each class fetches OUTSIDE
// any transaction and writes INSIDE its own transaction, mirroring the marker
// path's discipline.
func (t *markerSyncTask) applyExtras(ctx context.Context, id markersync.SceneIdentity) {
	if !t.syncURLs && !t.syncGalleries && !t.syncGroups && !t.matchFunscripts {
		return
	}

	r := instance.Repository
	for _, src := range t.sources {
		if !src.Enabled() {
			continue
		}

		if t.syncURLs {
			if p, ok := src.(markersync.ExtraURLProvider); ok {
				t.applyExtraURLs(ctx, r, src.Name(), p, id)
			}
		}
		if t.syncGalleries {
			if p, ok := src.(markersync.GalleryProvider); ok {
				t.applyGalleries(ctx, r, src.Name(), p, id)
			}
		}
		if t.syncGroups {
			if p, ok := src.(markersync.GroupProvider); ok {
				t.applyGroups(ctx, r, src.Name(), p, id)
			}
		}
		if t.matchFunscripts {
			if p, ok := src.(markersync.FunscriptProvider); ok {
				t.applyFunscripts(ctx, r, src.Name(), p, id)
			}
		}
	}
}

// applyFunscripts copies indexed funscripts whose md5 matches a source-provided
// funscript hash next to the scene's primary video file. The md5 lookups run in
// a read transaction; the file copy is a filesystem op performed OUTSIDE any
// transaction. An existing destination is never overwritten (protects
// user-authored scripts) - see copyFirstFunscript's dest-exists guard.
func (t *markerSyncTask) applyFunscripts(ctx context.Context, r models.Repository, srcName string, p markersync.FunscriptProvider, id markersync.SceneIdentity) {
	refs, err := p.FetchFunscripts(ctx, id)
	if err != nil {
		logger.Warnf("Marker Sync: scene %d: source %s: fetching funscripts: %v", t.scene.ID, srcName, err)
		return
	}
	if len(refs) == 0 {
		return
	}

	// Resolve the scene's primary video path (copy destination directory + stem).
	videoPath, err := t.funscriptVideoPath(ctx, r)
	if err != nil {
		logger.Warnf("Marker Sync: scene %d: source %s: resolving video path: %v", t.scene.ID, srcName, err)
		return
	}
	if videoPath == "" {
		return
	}

	// Gather candidate source funscript files (DB reads) INSIDE a read txn.
	var sources []string
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		for _, ref := range refs {
			if ref.MD5 == "" {
				continue
			}
			rows, err := r.FunscriptIndex.FindByMD5(ctx, ref.MD5)
			if err != nil {
				return fmt.Errorf("finding funscript by md5 %s: %w", ref.MD5, err)
			}
			for _, row := range rows {
				sources = append(sources, row.Filename)
			}
		}
		return nil
	}); err != nil {
		logger.Warnf("Marker Sync: scene %d: source %s: looking up funscripts: %v", t.scene.ID, srcName, err)
		return
	}
	if len(sources) == 0 {
		return
	}

	// Copy the first matching funscript OUTSIDE any transaction.
	dest, err := copyFirstFunscript(sources, videoPath)
	if err != nil {
		logger.Warnf("Marker Sync: scene %d: source %s: copying funscript: %v", t.scene.ID, srcName, err)
		return
	}
	if dest != "" {
		logger.Infof("Marker Sync: scene %d: source %s: copied funscript to %s", t.scene.ID, srcName, dest)
	}
}

// funscriptVideoPath resolves the scene's primary video file path, used as the
// funscript copy destination directory + stem. It uses the already-loaded
// transient Path when present, otherwise loads the primary file in a read txn.
func (t *markerSyncTask) funscriptVideoPath(ctx context.Context, r models.Repository) (string, error) {
	if t.scene.Path != "" {
		return t.scene.Path, nil
	}

	var path string
	err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		sc, err := r.Scene.Find(ctx, t.scene.ID)
		if err != nil {
			return err
		}
		if sc == nil {
			return nil
		}
		if sc.Path != "" {
			path = sc.Path
			return nil
		}
		if err := sc.LoadPrimaryFile(ctx, r.File); err != nil {
			return err
		}
		if pf := sc.Files.Primary(); pf != nil {
			path = pf.Base().Path
		}
		return nil
	})
	return path, err
}

// applyExtraURLs merges source-provided extra URLs into the scene's URLs,
// preserving existing order and de-duplicating. The scene is only updated when
// the merge actually adds a URL.
func (t *markerSyncTask) applyExtraURLs(ctx context.Context, r models.Repository, srcName string, p markersync.ExtraURLProvider, id markersync.SceneIdentity) {
	fetched, err := p.FetchExtraURLs(ctx, id)
	if err != nil {
		logger.Warnf("Marker Sync: scene %d: source %s: fetching extra urls: %v", t.scene.ID, srcName, err)
		return
	}
	if len(fetched) == 0 {
		return
	}

	var mergedCount int
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		sc, err := r.Scene.Find(ctx, t.scene.ID)
		if err != nil {
			return fmt.Errorf("loading scene: %w", err)
		}
		if sc == nil {
			return nil
		}
		if err := sc.LoadURLs(ctx, r.Scene); err != nil {
			return fmt.Errorf("loading scene urls: %w", err)
		}

		existing := sc.URLs.List()
		merged, added := mergeURLs(existing, fetched)
		if added == 0 {
			return nil
		}
		mergedCount = added

		_, err = r.Scene.UpdatePartial(ctx, t.scene.ID, models.ScenePartial{
			URLs: &models.UpdateStrings{Values: merged, Mode: models.RelationshipUpdateModeSet},
		})
		return err
	}); err != nil {
		logger.Errorf("Marker Sync: scene %d: source %s: applying extra urls: %v", t.scene.ID, srcName, err)
		return
	}

	if mergedCount > 0 {
		logger.Infof("Marker Sync: scene %d: source %s: merged %d extra url(s)", t.scene.ID, srcName, mergedCount)
	}
}

// applyGalleries links source-provided galleries (matched by file md5) to the
// scene, and back-fills gallery URLs when the gallery has none. Galleries
// bearing the skip-sync tag are ignored. It never creates galleries in Part A.
func (t *markerSyncTask) applyGalleries(ctx context.Context, r models.Repository, srcName string, p markersync.GalleryProvider, id markersync.SceneIdentity) {
	fetched, err := p.FetchGalleries(ctx, id)
	if err != nil {
		logger.Warnf("Marker Sync: scene %d: source %s: fetching galleries: %v", t.scene.ID, srcName, err)
		return
	}
	if len(fetched) == 0 {
		return
	}

	var linked int
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		// Resolve the skip-sync tag by name (never create it). 0 => not present.
		skipTagID := 0
		if tg, err := r.Tag.FindByName(ctx, ttSkipSyncTagName, true); err != nil {
			return fmt.Errorf("resolving skip-sync tag: %w", err)
		} else if tg != nil {
			skipTagID = tg.ID
		}

		seen := map[int]bool{}
		for _, ref := range fetched {
			for _, md5 := range ref.MD5s {
				gals, err := r.Gallery.FindByChecksum(ctx, md5)
				if err != nil {
					return fmt.Errorf("finding gallery by checksum %s: %w", md5, err)
				}
				for _, gal := range gals {
					if seen[gal.ID] {
						continue
					}
					seen[gal.ID] = true

					// Skip galleries tagged skip-sync.
					if skipTagID != 0 {
						if err := gal.LoadTagIDs(ctx, r.Gallery); err != nil {
							return fmt.Errorf("loading gallery %d tags: %w", gal.ID, err)
						}
						if slices.Contains(gal.TagIDs.List(), skipTagID) {
							continue
						}
					}

					partial := models.GalleryPartial{}
					needsUpdate := false

					if err := gal.LoadSceneIDs(ctx, r.Gallery); err != nil {
						return fmt.Errorf("loading gallery %d scenes: %w", gal.ID, err)
					}
					if !slices.Contains(gal.SceneIDs.List(), t.scene.ID) {
						partial.SceneIDs = &models.UpdateIDs{IDs: []int{t.scene.ID}, Mode: models.RelationshipUpdateModeAdd}
						needsUpdate = true
					}

					// Back-fill gallery URLs only when it has none of its own.
					if len(ref.URLs) > 0 {
						if err := gal.LoadURLs(ctx, r.Gallery); err != nil {
							return fmt.Errorf("loading gallery %d urls: %w", gal.ID, err)
						}
						if len(gal.URLs.List()) == 0 {
							partial.URLs = &models.UpdateStrings{Values: ref.URLs, Mode: models.RelationshipUpdateModeSet}
							needsUpdate = true
						}
					}

					if needsUpdate {
						if _, err := r.Gallery.UpdatePartial(ctx, gal.ID, partial); err != nil {
							return fmt.Errorf("updating gallery %d: %w", gal.ID, err)
						}
						linked++
					}
				}
			}
		}

		// TODO(stage5): auto-gallery create — the community plugin's
		// "[Timestamp: Auto Gallery]" create path is deferred to a later sub-stage.
		return nil
	}); err != nil {
		logger.Errorf("Marker Sync: scene %d: source %s: applying galleries: %v", t.scene.ID, srcName, err)
		return
	}

	if linked > 0 {
		logger.Infof("Marker Sync: scene %d: source %s: linked %d gallery/galleries", t.scene.ID, srcName, linked)
	}
}

// applyGroups creates and/or links source-provided groups to the scene. An
// existing group is matched by any of the ref's URLs before a new one is
// created, which makes the operation idempotent across re-syncs. The scene is
// attached to the group with the ref's scene index.
func (t *markerSyncTask) applyGroups(ctx context.Context, r models.Repository, srcName string, p markersync.GroupProvider, id markersync.SceneIdentity) {
	fetched, err := p.FetchGroups(ctx, id)
	if err != nil {
		logger.Warnf("Marker Sync: scene %d: source %s: fetching groups: %v", t.scene.ID, srcName, err)
		return
	}
	if len(fetched) == 0 {
		return
	}

	var created, linked int
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		// Load the scene's existing groups to avoid duplicate attachments.
		sc, err := r.Scene.Find(ctx, t.scene.ID)
		if err != nil {
			return fmt.Errorf("loading scene: %w", err)
		}
		if sc == nil {
			return nil
		}
		if err := sc.LoadGroups(ctx, r.Scene); err != nil {
			return fmt.Errorf("loading scene groups: %w", err)
		}
		attached := map[int]bool{}
		for _, gs := range sc.Groups.List() {
			attached[gs.GroupID] = true
		}

		for _, ref := range fetched {
			groupID, err := findGroupByURLs(ctx, r.Group, ref.URLs)
			if err != nil {
				return err
			}

			if groupID == 0 {
				name := ref.Name
				if name == "" {
					name = ref.ExternalID
				}
				if name == "" {
					// Nothing usable to name the group by; skip it.
					continue
				}

				g := models.NewGroup()
				g.Name = name
				g.Synopsis = ref.Synopsis
				if ref.Date != "" {
					if d, err := models.ParseDate(ref.Date); err == nil {
						g.Date = &d
					} else {
						logger.Debugf("Marker Sync: scene %d: source %s: unparseable group date %q: %v", t.scene.ID, srcName, ref.Date, err)
					}
				}
				g.URLs = models.NewRelatedStrings(ref.URLs)

				if err := r.Group.Create(ctx, &g); err != nil {
					return fmt.Errorf("creating group %q: %w", name, err)
				}
				groupID = g.ID
				created++

				// TODO(stage5): optional scraper enrichment — the community plugin's
				// scrape_movie_url path (front/back image, studio, director) is a
				// deferred nice-to-have; create-from-tt-data is sufficient here.
			}

			if attached[groupID] {
				continue
			}

			if _, err := r.Scene.UpdatePartial(ctx, t.scene.ID, models.ScenePartial{
				GroupIDs: &models.UpdateGroupIDs{
					Groups: []models.GroupsScenes{{GroupID: groupID, SceneIndex: ref.SceneIndex}},
					Mode:   models.RelationshipUpdateModeAdd,
				},
			}); err != nil {
				return fmt.Errorf("attaching group %d to scene: %w", groupID, err)
			}
			attached[groupID] = true
			linked++
		}
		return nil
	}); err != nil {
		logger.Errorf("Marker Sync: scene %d: source %s: applying groups: %v", t.scene.ID, srcName, err)
		return
	}

	if created > 0 || linked > 0 {
		logger.Infof("Marker Sync: scene %d: source %s: created %d group(s), linked %d", t.scene.ID, srcName, created, linked)
	}
}

// findGroupByURLs returns the id of the first existing group whose URL exactly
// matches any of the given urls, or 0 when none match.
func findGroupByURLs(ctx context.Context, qb models.GroupQueryer, urls []string) (int, error) {
	for _, u := range urls {
		if u == "" {
			continue
		}
		found, _, err := qb.Query(ctx, &models.GroupFilterType{
			URL: &models.StringCriterionInput{Value: u, Modifier: models.CriterionModifierEquals},
		}, nil)
		if err != nil {
			return 0, fmt.Errorf("querying group by url %q: %w", u, err)
		}
		if len(found) > 0 {
			return found[0].ID, nil
		}
	}
	return 0, nil
}

// mergeURLs returns existing with any new (non-empty, not-yet-present) urls
// from add appended, preserving order, along with the number of urls added.
func mergeURLs(existing, add []string) ([]string, int) {
	seen := make(map[string]bool, len(existing))
	merged := make([]string, 0, len(existing)+len(add))
	for _, u := range existing {
		if !seen[u] {
			seen[u] = true
			merged = append(merged, u)
		}
	}
	added := 0
	for _, u := range add {
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		merged = append(merged, u)
		added++
	}
	return merged, added
}

// MarkerSync launches a background job that synchronises scene markers from the
// configured external sources. It returns the job id.
func (s *Manager) MarkerSync(ctx context.Context, input MarkerSyncInput) int {
	j := job.MakeJobExec(func(ctx context.Context, progress *job.Progress) error {
		logger.Infof("Initiating marker sync")

		cfg := config.GetInstance().GetMarkerSyncConfig()
		sources := buildMarkerSyncSources()
		opts := markerSyncApplyOptions(cfg)
		fireHooks := cfg.FireHooks

		scenes, err := s.markerSyncScenes(ctx, input)
		if err != nil {
			return err
		}

		if len(scenes) == 0 {
			logger.Infof("Marker Sync: no matching scenes")
			return nil
		}

		progress.SetTotal(len(scenes))
		logger.Infof("Marker Sync: starting for %d scenes", len(scenes))

		for _, sc := range scenes {
			if job.IsCancelled(ctx) {
				logger.Info("Marker Sync: stopping due to user request")
				return nil
			}

			task := &markerSyncTask{
				scene:           sc,
				sources:         sources,
				opts:            opts,
				fireHooks:       fireHooks,
				syncURLs:        cfg.SyncURLs,
				syncGalleries:   cfg.SyncGalleries,
				syncGroups:      cfg.SyncGroups,
				matchFunscripts: cfg.MatchFunscripts,
			}
			progress.ExecuteTask(task.GetDescription(), func() {
				task.Start(ctx)
			})
			progress.Increment()
		}

		logger.Info("Marker Sync: finished")
		return nil
	})

	return s.JobManager.Add(ctx, "Marker Sync", j)
}

// markerSyncScenes resolves the scene set for the run, with stash ids loaded on
// each returned scene. Scenes bearing the skip-sync tag are excluded in every
// selection mode, mirroring how the submit path honours its skip tag. Scene
// resolution runs inside a read transaction.
func (s *Manager) markerSyncScenes(ctx context.Context, input MarkerSyncInput) ([]*models.Scene, error) {
	mode := input.resolveMode()

	var scenes []*models.Scene
	err := s.Repository.WithReadTxn(ctx, func(ctx context.Context) error {
		// Resolve the skip-sync tag once; id 0 means it does not exist and no
		// scene is skipped for it. The tag is never created here.
		skipTagID, err := s.markerSyncSkipSyncTagID(ctx)
		if err != nil {
			return err
		}

		// sceneHasSkipTag reports whether sc bears the skip-sync tag.
		sceneHasSkipTag := func(sc *models.Scene) (bool, error) {
			if skipTagID == 0 {
				return false, nil
			}
			if err := sc.LoadTagIDs(ctx, s.Repository.Scene); err != nil {
				return false, fmt.Errorf("loading tag ids for scene %d: %w", sc.ID, err)
			}
			return slices.Contains(sc.TagIDs.List(), skipTagID), nil
		}

		switch mode {
		case MarkerSyncSelectModeIds:
			ids, err := stringslice.StringSliceToIntSlice(input.SceneIDs)
			if err != nil {
				return fmt.Errorf("converting scene ids: %w", err)
			}
			found, err := s.SceneService.FindByIDs(ctx, ids, scene.LoadStashIDs)
			if err != nil {
				return fmt.Errorf("finding scenes by id: %w", err)
			}
			for _, sc := range found {
				if sc == nil {
					continue
				}
				skip, err := sceneHasSkipTag(sc)
				if err != nil {
					return err
				}
				if skip {
					continue
				}
				scenes = append(scenes, sc)
			}
			return nil

		case MarkerSyncSelectModeAll, MarkerSyncSelectModeOnlyWithoutMarkers:
			// NOTE: this loads the whole library into memory. Adequate for a
			// background job on typical libraries; a paged Scene.Query with a
			// server-side no-markers criterion is a follow-up for very large
			// libraries.
			all, err := s.Repository.Scene.All(ctx)
			if err != nil {
				return fmt.Errorf("loading all scenes: %w", err)
			}

			for _, sc := range all {
				// Honour job cancellation while scanning a large library.
				if err := ctx.Err(); err != nil {
					return err
				}
				if sc == nil {
					continue
				}
				if err := sc.LoadStashIDs(ctx, s.Repository.Scene); err != nil {
					return fmt.Errorf("loading stash ids for scene %d: %w", sc.ID, err)
				}

				skip, err := sceneHasSkipTag(sc)
				if err != nil {
					return err
				}
				if skip {
					continue
				}

				if mode == MarkerSyncSelectModeOnlyWithoutMarkers {
					existing, err := s.Repository.SceneMarker.FindBySceneID(ctx, sc.ID)
					if err != nil {
						return fmt.Errorf("counting markers for scene %d: %w", sc.ID, err)
					}
					if len(existing) > 0 {
						continue
					}
				}

				scenes = append(scenes, sc)
			}
			return nil

		default:
			return fmt.Errorf("%w: unknown marker sync mode %q", ErrInput, mode)
		}
	})

	return scenes, err
}

// markerSyncSkipSyncTagID returns the id of the skip-sync tag, or 0 when the tag
// does not exist. It must be called inside a read transaction. The tag is never
// created by this package; a missing tag simply means nothing is skipped.
func (s *Manager) markerSyncSkipSyncTagID(ctx context.Context) (int, error) {
	const nocase = true
	tag, err := s.Repository.Tag.FindByName(ctx, ttSkipSyncTagName, nocase)
	if err != nil {
		return 0, fmt.Errorf("finding skip-sync tag: %w", err)
	}
	if tag == nil {
		return 0, nil
	}
	return tag.ID, nil
}
