package manager

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/stashbox"
)

var errAuthoritativePerformerNotFound = errors.New("authoritative performer could not be mapped")

type scenePerformerLookup func(context.Context, models.StashBox, string) ([]*models.ScrapedPerformer, error)

func lookupStashBoxScenePerformers(ctx context.Context, box models.StashBox, sceneID string) ([]*models.ScrapedPerformer, error) {
	return stashbox.NewClient(box).FindScenePerformersByID(ctx, sceneID)
}

type authoritativePerformerResult struct {
	IDs  []int
	Mode ProviderFieldMode
}

// authoritativeScenePerformerIDs evaluates only explicit policies, ordered by
// priority. Registration and scene relationship order are never authority.
func (j *analyzeSceneMetadataJob) authoritativeScenePerformerIDs(ctx context.Context, scene *models.Scene) (authoritativePerformerResult, bool) {
	if scene == nil || len(scene.StashIDs.List()) == 0 || len(j.input.ProviderPolicies) == 0 {
		return authoritativePerformerResult{}, false
	}

	boxes := make(map[string]models.StashBox, len(j.configuredStashBoxes))
	for _, box := range j.configuredStashBoxes {
		if box != nil {
			boxes[normalizeStashBoxEndpoint(box.Endpoint)] = *box
		}
	}
	sceneIDs := make(map[string]string, len(scene.StashIDs.List()))
	for _, sceneID := range scene.StashIDs.List() {
		if strings.TrimSpace(sceneID.StashID) != "" {
			sceneIDs[normalizeStashBoxEndpoint(sceneID.Endpoint)] = strings.TrimSpace(sceneID.StashID)
		}
	}
	policies := append([]ProviderPolicy(nil), j.input.ProviderPolicies...)
	sort.SliceStable(policies, func(left, right int) bool {
		return policies[left].Priority < policies[right].Priority
	})
	lookup := j.scenePerformerLookup
	if lookup == nil {
		lookup = lookupStashBoxScenePerformers
	}

	for _, policy := range policies {
		if policy.PerformerMode != ProviderFieldModeMerge &&
			policy.PerformerMode != ProviderFieldModeReplace {
			continue
		}
		if policy.PerformerMode == ProviderFieldModeReplace && !j.input.ReplaceLocalPerformersFromRemote {
			continue
		}
		endpoint := normalizeStashBoxEndpoint(policy.Endpoint)
		box, configured := boxes[endpoint]
		sceneID, exactSceneID := sceneIDs[endpoint]
		if !configured || !exactSceneID {
			continue
		}
		remote, err := lookup(ctx, box, sceneID)
		if err != nil {
			if ctx.Err() != nil {
				return authoritativePerformerResult{}, false
			}
			logger.Warnf("[scene metadata] scene %d: performer policy lookup failed for %s: %v", scene.ID, box.Endpoint, err)
			continue
		}
		if len(remote) == 0 {
			continue
		}
		ids, complete := j.matchAuthoritativePerformers(ctx, box.Endpoint, remote)
		if complete {
			return authoritativePerformerResult{IDs: ids, Mode: policy.PerformerMode}, true
		}
		logger.Warnf("[scene metadata] scene %d: performer policy result from %s could not be mapped completely; preserving current performers", scene.ID, box.Endpoint)
	}
	return authoritativePerformerResult{}, false
}

func (j *analyzeSceneMetadataJob) providerPolicy(endpoint string) (ProviderPolicy, bool) {
	normalized := normalizeStashBoxEndpoint(endpoint)
	policies := append([]ProviderPolicy(nil), j.input.ProviderPolicies...)
	sort.SliceStable(policies, func(left, right int) bool {
		return policies[left].Priority < policies[right].Priority
	})
	for _, policy := range policies {
		if normalizeStashBoxEndpoint(policy.Endpoint) == normalized {
			return policy, true
		}
	}
	return ProviderPolicy{}, false
}

func (j *analyzeSceneMetadataJob) matchAuthoritativePerformers(ctx context.Context, endpoint string, remote []*models.ScrapedPerformer) ([]int, bool) {
	ids := make([]int, 0, len(remote))
	seen := make(map[int]struct{}, len(remote))
	err := j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		for _, scraped := range remote {
			id, found, err := j.matchAuthoritativePerformer(ctx, endpoint, scraped)
			if err != nil {
				return err
			}
			if !found {
				return errAuthoritativePerformerNotFound
			}
			if _, duplicate := seen[id]; duplicate {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
		return nil
	})
	return ids, err == nil && len(ids) > 0
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
