package manager

import (
	"context"
	"errors"
	"strings"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/performer"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/stashbox"
)

var errAuthoritativePerformerNotFound = errors.New("authoritative performer could not be mapped")

type scenePerformerLookup func(context.Context, models.StashBox, string) ([]*models.ScrapedPerformer, error)

func lookupStashBoxScenePerformers(ctx context.Context, box models.StashBox, sceneID string) ([]*models.ScrapedPerformer, error) {
	return stashbox.NewClient(box).FindScenePerformersByID(ctx, sceneID)
}

// authoritativeScenePerformerIDs resolves the performer list attached to a
// scene's existing Stash-box ID. It only returns an authoritative result when
// every remote performer maps unambiguously to a local performer; partial
// results must never erase correct local relationships.
func (j *analyzeSceneMetadataJob) authoritativeScenePerformerIDs(ctx context.Context, scene *models.Scene) ([]int, bool) {
	if scene == nil || len(scene.StashIDs.List()) == 0 {
		return nil, false
	}

	boxes := make(map[string]models.StashBox, len(j.configuredStashBoxes))
	for _, box := range j.configuredStashBoxes {
		if box == nil {
			continue
		}
		boxes[normalizeStashBoxEndpoint(box.Endpoint)] = *box
	}
	lookup := j.scenePerformerLookup
	if lookup == nil {
		lookup = lookupStashBoxScenePerformers
	}

	for _, sceneStashID := range scene.StashIDs.List() {
		box, found := boxes[normalizeStashBoxEndpoint(sceneStashID.Endpoint)]
		if !found || strings.TrimSpace(sceneStashID.StashID) == "" {
			continue
		}

		remote, err := lookup(ctx, box, sceneStashID.StashID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, false
			}
			logger.Warnf("[scene metadata] scene %d: authoritative performer lookup failed for %s: %v", scene.ID, box.Endpoint, err)
			continue
		}
		if len(remote) == 0 {
			continue
		}

		ids, complete := j.matchAuthoritativePerformers(ctx, box.Endpoint, remote)
		if complete {
			return ids, true
		}
		logger.Warnf("[scene metadata] scene %d: authoritative performer list from %s could not be mapped completely; preserving current performers", scene.ID, box.Endpoint)
	}

	return nil, false
}

func (j *analyzeSceneMetadataJob) matchAuthoritativePerformers(ctx context.Context, endpoint string, remote []*models.ScrapedPerformer) ([]int, bool) {
	ids := make([]int, 0, len(remote))
	seen := make(map[int]struct{}, len(remote))
	var created []*models.Performer

	err := j.repository.WithTxn(ctx, func(ctx context.Context) error {
		for _, scraped := range remote {
			id, found, err := j.matchAuthoritativePerformer(ctx, endpoint, scraped)
			if err != nil {
				return err
			}
			if !found {
				if j.input.DryRun || scraped == nil || scraped.Name == nil || strings.TrimSpace(*scraped.Name) == "" {
					return errAuthoritativePerformerNotFound
				}
				newPerformer := scraped.ToPerformer(endpoint, nil)
				if err := performer.ValidateCreate(ctx, *newPerformer, j.repository.Performer); err != nil {
					return err
				}
				if err := j.repository.Performer.Create(ctx, &models.CreatePerformerInput{Performer: newPerformer}); err != nil {
					return err
				}
				created = append(created, newPerformer)
				id = newPerformer.ID
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil || len(ids) == 0 {
		return ids, false
	}
	for _, p := range created {
		j.performerRecords = append(j.performerRecords, metadata.NamedAliases{
			ID: p.ID, Name: p.Name,
		})
		logger.Infof("[scene metadata] created authoritative performer %q from %s", p.Name, endpoint)
	}
	return ids, true
}

func (j *analyzeSceneMetadataJob) matchAuthoritativePerformer(ctx context.Context, endpoint string, scraped *models.ScrapedPerformer) (int, bool, error) {
	if scraped == nil {
		return 0, false, nil
	}
	if scraped.RemoteSiteID != nil && strings.TrimSpace(*scraped.RemoteSiteID) != "" {
		matches, err := j.repository.Performer.FindByStashID(ctx, models.StashID{
			Endpoint: endpoint,
			StashID:  strings.TrimSpace(*scraped.RemoteSiteID),
		})
		if err != nil {
			return 0, false, err
		}
		if len(matches) == 1 {
			return matches[0].ID, true, nil
		}
		if len(matches) > 1 {
			return 0, false, nil
		}
	}
	if scraped.Name == nil || strings.TrimSpace(*scraped.Name) == "" {
		return 0, false, nil
	}
	matches, err := findExactPerformerIdentities(ctx, j.repository.Performer, *scraped.Name)
	if err != nil {
		return 0, false, err
	}
	if len(matches) != 1 {
		return 0, false, nil
	}
	return matches[0].ID, true, nil
}

func normalizeStashBoxEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

func sameIDSet(left, right []int) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[int]int, len(left))
	for _, id := range left {
		seen[id]++
	}
	for _, id := range right {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}
