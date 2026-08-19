// Package tagging joins the AI tagging pipeline to Stash: it runs a provider
// over a scene, stores the raw spans, and writes markers back.
//
// The split is deliberate. pkg/aitag knows how to analyse and how to cluster,
// and knows nothing about Stash; this package knows about scenes, tags, markers
// and the AI database, and knows nothing about how inference works. Swapping
// the provider changes nothing here.
package tagging

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/txn"
)

// WritebackOptions control how markers and scene tags reach Stash.
type WritebackOptions struct {
	// CreateMissingTags creates a Stash tag when a canonical label has no
	// matching name or alias.
	CreateMissingTags bool

	// ApplySceneTags adds every resolved marker tag to the scene without
	// removing tags the user already applied.
	ApplySceneTags bool
	// SceneTagNames are the accepted labels to apply to the scene. Marker
	// renames may be used for labels that produced markers; labels with no
	// marker rule retain their canonical provider name.
	SceneTagNames []string

	// ReplacePrevious removes the markers a previous run of the same service
	// created for the scene. On by default - without it a re-analysis leaves
	// two overlapping sets.
	ReplacePrevious bool

	// DryRun computes everything and writes nothing, which is what makes it
	// possible to see what a re-tune would do before committing to it.
	DryRun bool
}

// DefaultWritebackOptions is the safe configuration: replace our own markers,
// touch nothing else, create no tags.
func DefaultWritebackOptions() WritebackOptions {
	return WritebackOptions{ReplacePrevious: true}
}

// WritebackResult reports what a writeback did.
type WritebackResult struct {
	Created int `json:"created"`
	Removed int `json:"removed"`
	// TagsApplied counts distinct resolved tags added to the scene.
	TagsApplied int `json:"tags_applied"`
	// TagsCreated counts canonical labels that required a new Stash tag.
	TagsCreated int `json:"tags_created"`
	// SkippedNoTag counts markers dropped because their label has no Stash tag
	// and tag creation was not requested. Reported rather than silently
	// swallowed: it is the most common reason a run "produces nothing".
	SkippedNoTag int `json:"skipped_no_tag"`
	// MissingTags names those labels, so the user can create them or turn the
	// option on.
	MissingTags []string `json:"missing_tags,omitempty"`
	DryRun      bool     `json:"dry_run"`
}

// Writer creates markers in Stash from clustered AI results.
type Writer struct {
	repo models.Repository
	db   *store.DB

	// tagCache avoids a lookup per marker; a scene commonly produces dozens of
	// markers across a handful of distinct tags.
	mu       sync.Mutex
	tagCache map[string]*int
}

// NewWriter builds a writer.
func NewWriter(repo models.Repository, db *store.DB) *Writer {
	return &Writer{repo: repo, db: db, tagCache: map[string]*int{}}
}

// Write turns markers into Stash scene markers.
//
// Idempotent by construction: the markers this service previously created for
// the scene are recorded in the AI database, so they can be removed precisely.
// Markers the user placed by hand are never touched, whatever their tag.
func (w *Writer) Write(ctx context.Context, service string, sceneID int, runID int64, markers []aitag.Marker, frameInterval float64, opts WritebackOptions) (WritebackResult, error) {
	result := WritebackResult{DryRun: opts.DryRun}

	if w.repo.SceneMarker == nil {
		return result, fmt.Errorf("scene markers are unavailable")
	}
	if opts.ApplySceneTags && w.repo.Scene == nil {
		return result, fmt.Errorf("scenes are unavailable")
	}

	// Resolve tags first, outside the write transaction: a missing tag is a
	// reason to report rather than to abort a partly-applied write.
	type resolved struct {
		marker aitag.Marker
		tagID  int
	}

	var (
		toCreate   []resolved
		missing    = map[string]bool{}
		sceneTagID = map[int]bool{}
		createdTag = map[int]bool{}
	)

	for _, marker := range markers {
		tagID, created, err := w.resolveTag(ctx, marker.RenamedTag, opts.CreateMissingTags)
		if err != nil {
			return result, err
		}
		if tagID == nil {
			result.SkippedNoTag++
			missing[marker.RenamedTag] = true
			continue
		}
		toCreate = append(toCreate, resolved{marker: marker, tagID: *tagID})
		if opts.ApplySceneTags && len(opts.SceneTagNames) == 0 {
			sceneTagID[*tagID] = true
		}
		if created {
			createdTag[*tagID] = true
		}
	}

	if opts.ApplySceneTags {
		for _, name := range opts.SceneTagNames {
			tagID, created, err := w.resolveTag(ctx, name, opts.CreateMissingTags)
			if err != nil {
				return result, err
			}
			if tagID == nil {
				missing[name] = true
				continue
			}
			sceneTagID[*tagID] = true
			if created {
				createdTag[*tagID] = true
			}
		}
	}

	for name := range missing {
		result.MissingTags = append(result.MissingTags, name)
	}
	sort.Strings(result.MissingTags)
	result.TagsCreated = len(createdTag)

	var previous []int
	if opts.ReplacePrevious && w.db != nil {
		var err error
		previous, err = w.db.PreviousMarkers(ctx, service, sceneID)
		if err != nil {
			return result, fmt.Errorf("read previous markers: %w", err)
		}
	}

	if opts.DryRun {
		result.Created = len(toCreate)
		if opts.ApplySceneTags {
			result.TagsApplied = len(sceneTagID)
		}
		result.Removed = len(previous)
		return result, nil
	}

	var written []store.WrittenMarker

	err := txn.WithTxn(ctx, w.repo.TxnManager, func(ctx context.Context) error {
		for _, id := range previous {
			if err := w.repo.SceneMarker.Destroy(ctx, id); err != nil {
				// A marker the user already deleted is not a failure. Stash
				// reports that as an error, and treating it as one would make
				// every re-run after a manual tidy-up fail.
				logger.Debugf("AI marker %d was already gone: %v", id, err)
				continue
			}
			result.Removed++
		}

		for _, entry := range toCreate {
			end := entry.marker.End + frameInterval

			marker := models.NewSceneMarker()
			marker.SceneID = sceneID
			marker.PrimaryTagID = entry.tagID
			marker.Seconds = entry.marker.Start
			marker.EndSeconds = &end
			// The title is left empty so Stash renders the tag's name, which is
			// what a hand-made marker with no title does too.

			if err := w.repo.SceneMarker.Create(ctx, &marker); err != nil {
				return fmt.Errorf("create marker for %s: %w", entry.marker.RenamedTag, err)
			}

			written = append(written, store.WrittenMarker{
				MarkerID: marker.ID,
				SceneID:  sceneID,
				Service:  service,
				TagName:  entry.marker.RenamedTag,
				Start:    marker.Seconds,
				End:      marker.EndSeconds,
			})
			result.Created++
		}

		if opts.ApplySceneTags && len(sceneTagID) > 0 {
			tagIDs := make([]int, 0, len(sceneTagID))
			for id := range sceneTagID {
				tagIDs = append(tagIDs, id)
			}
			sort.Ints(tagIDs)
			partial := models.NewScenePartial()
			partial.TagIDs = &models.UpdateIDs{
				IDs:  tagIDs,
				Mode: models.RelationshipUpdateModeAdd,
			}
			if _, err := w.repo.Scene.UpdatePartial(ctx, sceneID, partial); err != nil {
				return fmt.Errorf("apply AI tags to scene: %w", err)
			}
			result.TagsApplied = len(tagIDs)
		}
		return nil
	})
	if err != nil {
		return result, err
	}

	if w.db != nil {
		// Provenance is recorded AFTER the markers exist. If this fails the
		// markers are real but unrecorded, so the next run will not remove
		// them - visible duplicates, which is far better than the reverse:
		// recording first and failing to create would make the next run delete
		// marker ids belonging to something else.
		if len(previous) > 0 {
			if err := w.db.ForgetMarkers(ctx, previous); err != nil {
				logger.Errorf("could not forget replaced AI markers: %v", err)
			}
		}
		if err := w.db.RecordMarkers(ctx, runID, written); err != nil {
			logger.Errorf("could not record AI marker provenance: %v", err)
		}
	}

	return result, nil
}

// resolveTag maps a label to a Stash tag id, optionally creating it. The bool
// reports whether this call created the tag.
func (w *Writer) resolveTag(ctx context.Context, name string, create bool) (*int, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, false, nil
	}

	w.mu.Lock()
	if cached, ok := w.tagCache[name]; ok && (cached != nil || !create) {
		w.mu.Unlock()
		return cached, false, nil
	}
	w.mu.Unlock()

	if w.repo.Tag == nil {
		return nil, false, nil
	}

	var found *models.Tag
	err := txn.WithReadTxn(ctx, w.repo.TxnManager, func(ctx context.Context) error {
		// Case-insensitive: a user's "Blowjob_AI" and a model's "blowjob_ai"
		// are the same tag as far as anyone is concerned.
		tag, err := w.repo.Tag.FindByName(ctx, name, true)
		if err != nil {
			return err
		}
		found = tag
		if found != nil {
			return nil
		}
		// An alias is how a user maps a generated name onto a tag they already
		// have, so it has to be checked before concluding the tag is missing.
		tag, err = w.repo.Tag.FindByAlias(ctx, name, true)
		if err != nil {
			return err
		}
		found = tag
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("look up tag %q: %w", name, err)
	}

	created := false
	if found == nil && create {
		newTag := models.NewTag()
		newTag.Name = name
		input := &models.CreateTagInput{Tag: &newTag}
		err := txn.WithTxn(ctx, w.repo.TxnManager, func(ctx context.Context) error {
			return w.repo.Tag.Create(ctx, input)
		})
		if err != nil {
			return nil, false, fmt.Errorf("create tag %q: %w", name, err)
		}
		found = &newTag
		created = true
	}

	var id *int
	if found != nil {
		value := found.ID
		id = &value
	}

	w.mu.Lock()
	w.tagCache[name] = id
	w.mu.Unlock()
	return id, created, nil
}

// InvalidateTagCache forgets resolved tags.
//
// Called after a run: a user who creates the missing tag the report named
// expects the next run to find it, and a cache that outlived the run would make
// them restart Stash to get that.
func (w *Writer) InvalidateTagCache() {
	w.mu.Lock()
	w.tagCache = map[string]*int{}
	w.mu.Unlock()
}
