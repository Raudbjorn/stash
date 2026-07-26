package markersync

import (
	"context"
	"fmt"
	"math"
)

// Default apply parameters.
const (
	// DefaultTolerance is the default matching window, in seconds, within which
	// a candidate is considered a duplicate of an existing marker. It mirrors
	// stashapp-tools' import_scene_markers(..., 15).
	DefaultTolerance = 15.0
	// ModeSkip skips candidates that duplicate an existing marker.
	ModeSkip = "skip"
	// ModeMerge creates only candidates that do not duplicate an existing
	// marker (equivalent to skip for duplicates).
	ModeMerge = "merge"
	// ModeOverwrite deletes matched existing duplicates and recreates them.
	ModeOverwrite = "overwrite"
)

// ExistingMarker is the minimal projection of a persisted scene marker required
// by the dedup engine. Seconds is in seconds.
type ExistingMarker struct {
	ID           int
	Seconds      float64
	PrimaryTagID int
}

// MarkerWriter is the persistence boundary for the apply engine. The concrete
// implementation backed by the scene-marker repository lives in the manager
// layer (Stage 2).
type MarkerWriter interface {
	// ExistingForScene returns the markers already persisted for the scene.
	ExistingForScene(ctx context.Context, sceneID int) ([]ExistingMarker, error)
	// CreateMarker persists a new marker and returns its id.
	CreateMarker(ctx context.Context, sceneID int, title string, seconds float64, endSeconds *float64, primaryTagID int, extraTagIDs []int) (createdID int, err error)
	// DeleteMarker deletes the marker with the given id.
	DeleteMarker(ctx context.Context, id int) error
}

// ApplyOptions controls the dedup/apply behaviour.
type ApplyOptions struct {
	// Tolerance is the duplicate matching window in seconds. Values <= 0 fall
	// back to DefaultTolerance.
	Tolerance float64
	// Mode is one of ModeSkip, ModeMerge or ModeOverwrite. An empty value
	// defaults to ModeSkip.
	Mode string
	// SkipTags is a set of primary tag names whose candidates are ignored
	// entirely. Matching is exact.
	SkipTags []string
	// TagAware, when true, requires a matching primary tag (in addition to the
	// time window) for a candidate to count as a duplicate. When false, the
	// time window alone determines duplicates.
	//
	// Note: the zero value is false. Callers wanting the recommended
	// tag-aware behaviour must set this to true explicitly.
	TagAware bool
}

// ApplyResult reports what the apply engine did.
type ApplyResult struct {
	// Created is the number of brand new markers created (no duplicate existed).
	Created int
	// Skipped is the number of candidates skipped because they duplicated an
	// existing marker (skip/merge modes).
	Skipped int
	// Overwritten is the number of candidates that replaced existing duplicates
	// (overwrite mode).
	Overwritten int
	// CreatedIDs holds the ids of every marker created, including overwrite
	// replacements.
	CreatedIDs []int
}

// Apply reconciles candidate markers against the scene's existing markers. It
// reimplements stashapp-tools' import_scene_markers dedup logic.
//
// For each candidate: its primary tag is resolved to an id; candidates whose
// primary tag is in opts.SkipTags are ignored. A candidate duplicates an
// existing marker iff abs(existing.Seconds-candidate.Seconds) <= Tolerance and
// (!TagAware or existing.PrimaryTagID == candidate primary tag id).
//
//   - skip / merge: duplicates are skipped, non-duplicates are created.
//   - overwrite:    matched duplicates are deleted and the candidate is created.
func Apply(ctx context.Context, w MarkerWriter, tags TagResolver, sceneID int, candidates []MarkerCandidate, opts ApplyOptions) (ApplyResult, error) {
	var result ApplyResult

	tolerance := opts.Tolerance
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	mode := opts.Mode
	if mode == "" {
		mode = ModeSkip
	}

	skipTags := make(map[string]struct{}, len(opts.SkipTags))
	for _, t := range opts.SkipTags {
		skipTags[t] = struct{}{}
	}

	existing, err := w.ExistingForScene(ctx, sceneID)
	if err != nil {
		return result, fmt.Errorf("loading existing markers for scene %d: %w", sceneID, err)
	}

	for _, c := range candidates {
		if _, skip := skipTags[c.PrimaryTag]; skip {
			continue
		}

		primaryTagID, err := tags.ResolveOrCreate(ctx, c.PrimaryTag)
		if err != nil {
			return result, fmt.Errorf("resolving primary tag %q: %w", c.PrimaryTag, err)
		}

		dupIDs := findDuplicates(existing, c.Seconds, primaryTagID, tolerance, opts.TagAware)

		if len(dupIDs) > 0 && mode != ModeOverwrite {
			// skip / merge: leave the existing marker in place.
			result.Skipped++
			continue
		}

		extraTagIDs, err := resolveExtraTags(ctx, tags, c.ExtraTags, primaryTagID)
		if err != nil {
			return result, err
		}

		if len(dupIDs) > 0 {
			// overwrite: delete the matched existing markers first.
			for _, id := range dupIDs {
				if err := w.DeleteMarker(ctx, id); err != nil {
					return result, fmt.Errorf("deleting marker %d: %w", id, err)
				}
			}
		}

		createdID, err := w.CreateMarker(ctx, sceneID, c.Title, c.Seconds, c.EndSeconds, primaryTagID, extraTagIDs)
		if err != nil {
			return result, fmt.Errorf("creating marker %q: %w", c.Title, err)
		}
		result.CreatedIDs = append(result.CreatedIDs, createdID)

		if len(dupIDs) > 0 {
			result.Overwritten++
		} else {
			result.Created++
		}
	}

	return result, nil
}

// findDuplicates returns the ids of existing markers that duplicate a candidate
// at the given seconds/primary tag under the tolerance and tag-awareness rules.
func findDuplicates(existing []ExistingMarker, seconds float64, primaryTagID int, tolerance float64, tagAware bool) []int {
	var dupIDs []int
	for _, e := range existing {
		if math.Abs(e.Seconds-seconds) > tolerance {
			continue
		}
		if tagAware && e.PrimaryTagID != primaryTagID {
			continue
		}
		dupIDs = append(dupIDs, e.ID)
	}
	return dupIDs
}

// resolveExtraTags resolves extra tag names to ids, excluding the primary tag
// and de-duplicating.
func resolveExtraTags(ctx context.Context, tags TagResolver, names []string, primaryTagID int) ([]int, error) {
	if len(names) == 0 {
		return nil, nil
	}

	seen := map[int]struct{}{primaryTagID: {}}
	ids := make([]int, 0, len(names))
	for _, name := range names {
		id, err := tags.ResolveOrCreate(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("resolving extra tag %q: %w", name, err)
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, nil
}
