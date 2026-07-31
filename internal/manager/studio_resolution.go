package manager

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stashapp/stash/pkg/stashbox"
)

type studioResolutionStatus string

const (
	studioResolutionExisting   studioResolutionStatus = "existing"
	studioResolutionCreated    studioResolutionStatus = "created"
	studioResolutionUnverified studioResolutionStatus = "unverified"
	studioResolutionDryRun     studioResolutionStatus = "dry_run"
	studioResolutionCancelled  studioResolutionStatus = "cancelled"
	studioResolutionAmbiguous  studioResolutionStatus = "ambiguous"
)

const (
	studioReasonSingleExactLibraryMatch  = "single_exact_library_match"
	studioReasonMultipleLibraryMatches   = "multiple_exact_library_matches"
	studioReasonVerifierFailed           = "verifier_failed"
	studioReasonProviderNoResult         = "provider_no_result"
	studioReasonProviderUnavailable      = "provider_unavailable"
	studioReasonProviderResultsConflict  = "provider_results_conflict"
	studioReasonLocalAIUnavailable       = "local_ai_unavailable"
	studioReasonLocalAIInvalid           = "local_ai_invalid"
	studioReasonLocalAIRejected          = "local_ai_rejected"
	studioReasonDryRun                   = "dry_run"
	studioReasonCancelled                = "cancelled"
	studioReasonCreatedAfterVerification = "created_after_verification"
)

type studioResolution struct {
	Candidate   string
	Status      studioResolutionStatus
	StudioID    int
	MatchingIDs []int
	ProviderID  string
	Reason      string
}

type studioIdentity struct {
	ID      int
	Name    string
	Aliases []string
	URLs    []string
}

type studioStashBoxQuerier interface {
	FindStudio(context.Context, string) (*models.ScrapedStudio, error)
}

type studioStashBoxVerifier struct {
	ID     string
	client studioStashBoxQuerier
}

func newStudioStashBoxVerifier(box models.StashBox) studioStashBoxVerifier {
	return studioStashBoxVerifier{
		ID:     "stashbox:" + box.Endpoint,
		client: stashbox.NewClient(box),
	}
}

// studioMetadataProvider is a clean abstraction over "anything that
// can resolve a studio name to a ScrapedStudio". Today the only
// meaningful implementation is a Stash-box instance; a future
// ScrapeContentTypeStudio scraper plugs in here without changing
// the resolver.
type studioMetadataProvider struct {
	ID    string
	Name  string
	Query func(ctx context.Context, name string) (*models.ScrapedStudio, error)
}

func (p studioMetadataProvider) display() string {
	if p.Name == "" || p.Name == p.ID {
		return p.ID
	}
	return p.ID + " (" + p.Name + ")"
}

func (v studioStashBoxVerifier) provider(box *models.StashBox) studioMetadataProvider {
	name := ""
	if box != nil {
		name = strings.TrimSpace(box.Name)
	}
	return studioMetadataProvider{
		ID:   v.ID,
		Name: name,
		Query: func(ctx context.Context, queryName string) (*models.ScrapedStudio, error) {
			return v.client.FindStudio(ctx, queryName)
		},
	}
}

func scraperStudioProvider(s *scraper.Scraper) (studioMetadataProvider, bool) {
	if s == nil || s.Studio == nil {
		return studioMetadataProvider{}, false
	}
	for _, t := range s.Studio.SupportedScrapes {
		if t == scraper.ScrapeTypeName {
			id := s.ID
			return studioMetadataProvider{
				ID:   id,
				Name: s.Name,
				Query: func(ctx context.Context, queryName string) (*models.ScrapedStudio, error) {
					content, err := instance.ScraperCache.ScrapeName(ctx, id, queryName, scraper.ScrapeContentTypeStudio)
					if err != nil {
						return nil, err
					}
					for _, c := range content {
						if studio, ok := c.(*models.ScrapedStudio); ok {
							return studio, nil
						}
					}
					return nil, nil
				},
			}, true
		}
	}
	return studioMetadataProvider{}, false
}

func (j *analyzeSceneMetadataJob) resolveStudioVerifierScrapers() {
	j.studioVerifierScrapers = nil
	if j.scraperCache == nil {
		return
	}

	available := j.scraperCache.ListScrapers([]scraper.ScrapeContentType{scraper.ScrapeContentTypeStudio})
	byID := make(map[string]*scraper.Scraper, len(available))
	for _, s := range available {
		if s != nil {
			byID[s.ID] = s
		}
	}

	seen := make(map[string]struct{}, len(j.input.StudioVerifierScraperIDs))
	for _, id := range j.input.StudioVerifierScraperIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}

		s, found := byID[id]
		if !found {
			logger.Warnf("[scene metadata] studio verifier scraper %q is unavailable; skipping", id)
			continue
		}
		if !supportsStudioNameScrape(s) {
			logger.Warnf("[scene metadata] studio verifier scraper %q does not support studio name searches; skipping", id)
			continue
		}
		j.studioVerifierScrapers = append(j.studioVerifierScrapers, s)
	}
}

func supportsStudioNameScrape(s *scraper.Scraper) bool {
	if s == nil || s.Studio == nil {
		return false
	}
	for _, t := range s.Studio.SupportedScrapes {
		if t == scraper.ScrapeTypeName {
			return true
		}
	}
	return false
}

func (j *analyzeSceneMetadataJob) resolveStudioVerifierStashBoxes() {
	j.studioVerifierStashBoxes = nil
	byEndpoint := make(map[string]*models.StashBox, len(j.configuredStashBoxes))
	for _, box := range j.configuredStashBoxes {
		if box != nil && strings.TrimSpace(box.Endpoint) != "" {
			byEndpoint[box.Endpoint] = box
		}
	}

	seen := make(map[string]struct{}, len(j.input.StudioVerifierStashBoxEndpoints))
	for _, endpoint := range j.input.StudioVerifierStashBoxEndpoints {
		endpoint = strings.TrimSpace(endpoint)
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		seen[endpoint] = struct{}{}

		box, found := byEndpoint[endpoint]
		if !found {
			logger.Warnf("[scene metadata] studio verifier Stash-box endpoint %q is unavailable; skipping", endpoint)
			continue
		}
		j.studioVerifierStashBoxes = append(j.studioVerifierStashBoxes, newStudioStashBoxVerifier(*box))
	}
}

func (j *analyzeSceneMetadataJob) studioProviders() []studioMetadataProvider {
	providers := make([]studioMetadataProvider, 0, len(j.studioVerifierStashBoxes)+len(j.studioVerifierScrapers))
	byID := make(map[string]*models.StashBox, len(j.configuredStashBoxes))
	for _, box := range j.configuredStashBoxes {
		if box != nil {
			byID[box.Endpoint] = box
		}
	}
	for _, v := range j.studioVerifierStashBoxes {
		box := byID[strings.TrimPrefix(v.ID, "stashbox:")]
		providers = append(providers, v.provider(box))
	}
	for _, s := range j.studioVerifierScrapers {
		if p, ok := scraperStudioProvider(s); ok {
			providers = append(providers, p)
		}
	}
	return providers
}

func (j *analyzeSceneMetadataJob) studioPoolAvailable() bool {
	return len(j.studioVerifierStashBoxes) > 0 || len(j.studioVerifierScrapers) > 0
}

func deduplicateStudioCandidates(candidates []string) []string {
	ret := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		key := metadata.NormalizeKey(candidate)
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		ret = append(ret, candidate)
	}
	return ret
}

func findExactStudioIdentities(ctx context.Context, r models.StudioReader, candidate string) ([]studioIdentity, error) {
	var found []*models.Studio
	if isASCII(candidate) {
		pp := 50
		byName, _, err := r.Query(ctx, &models.StudioFilterType{
			Name: &models.StringCriterionInput{
				Value:    candidate,
				Modifier: models.CriterionModifierEquals,
			},
		}, &models.FindFilterType{PerPage: &pp})
		if err != nil {
			return nil, err
		}
		found = append(found, byName...)
		byAlias, _, err := r.Query(ctx, &models.StudioFilterType{
			Aliases: &models.StringCriterionInput{
				Value:    candidate,
				Modifier: models.CriterionModifierEquals,
			},
		}, &models.FindFilterType{PerPage: &pp})
		if err != nil {
			return nil, err
		}
		seen := make(map[int]struct{}, len(found))
		for _, s := range found {
			if s != nil {
				seen[s.ID] = struct{}{}
			}
		}
		for _, s := range byAlias {
			if s == nil {
				continue
			}
			if _, dup := seen[s.ID]; dup {
				continue
			}
			seen[s.ID] = struct{}{}
			found = append(found, s)
		}
	} else {
		all, err := r.All(ctx)
		if err != nil {
			return nil, err
		}
		found = all
	}

	candidateKey := metadata.NormalizeKey(candidate)
	byID := make(map[int]studioIdentity, len(found))
	for _, current := range found {
		if current == nil {
			continue
		}
		if _, exists := byID[current.ID]; exists {
			continue
		}
		if err := current.LoadAliases(ctx, r); err != nil {
			return nil, err
		}
		aliases := append([]string(nil), current.Aliases.List()...)
		if metadata.NormalizeKey(current.Name) != candidateKey && !containsNormalized(aliases, candidateKey) {
			continue
		}
		if err := current.LoadURLs(ctx, r); err != nil {
			return nil, err
		}
		byID[current.ID] = studioIdentity{
			ID:      current.ID,
			Name:    current.Name,
			Aliases: aliases,
			URLs:    append([]string(nil), current.URLs.List()...),
		}
	}
	ret := make([]studioIdentity, 0, len(byID))
	for _, identity := range byID {
		ret = append(ret, identity)
	}
	sort.Slice(ret, func(i, k int) bool { return ret[i].ID < ret[k].ID })
	return ret, nil
}

// studioProviderQueryResult is one provider's outcome from a fan-out
// query. The provider's id is preserved alongside the result so the
// LLM (when invoked) and the log line can attribute the source.
type studioProviderQueryResult struct {
	ProviderID string
	Studio     *models.ScrapedStudio
	Err        error
	HasResult  bool
}

// queryAllStudioProviders fans out to every configured provider
// (Stash-box + studio-by-name scraper) and collects their outcomes.
// The fan-out is sequential: studio lookups are O(few) and serial
// queries keep the error surface small and predictable.
func (j *analyzeSceneMetadataJob) queryAllStudioProviders(ctx context.Context, candidate string) ([]studioProviderQueryResult, error) {
	providers := j.studioProviders()
	results := make([]studioProviderQueryResult, 0, len(providers))
	for _, p := range providers {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		scraped, err := p.Query(ctx, candidate)
		out := studioProviderQueryResult{ProviderID: p.ID, Studio: scraped, Err: err}
		out.HasResult = scraped != nil
		results = append(results, out)
	}
	return results, nil
}

// reconcileStudioProviderResults picks the studio to adopt from a
// fan-out. Returns (scraped, providerID, "") on success, or
// (nil, "", reason) on a structured refusal. The empty reason
// means "use the scraped". The order of preference is:
//
//  1. If no provider returned a usable result, "no_result".
//  2. If exactly one provider returned a usable result, use it.
//  3. If multiple providers agree on the same canonical name,
//     use that one.
//  4. If multiple providers disagree, the result is ambiguous -
//     and if the LLM mode is on, the model picks the most
//     plausible one.
//  5. If the LLM is unavailable, the result stays unverified
//     with a dedicated reason so the user can fix their setup.
func (j *analyzeSceneMetadataJob) reconcileStudioProviderResults(ctx context.Context, candidate string, results []studioProviderQueryResult, sources []metadata.Source) (*models.ScrapedStudio, string, string) {
	var usable []studioProviderQueryResult
	for _, r := range results {
		if r.Err != nil || !r.HasResult {
			continue
		}
		if !studioCandidateMatchesScraped(candidate, r.Studio) {
			continue
		}
		usable = append(usable, r)
	}
	if len(usable) == 0 {
		return nil, "", studioReasonProviderNoResult
	}
	if len(usable) == 1 {
		return usable[0].Studio, usable[0].ProviderID, ""
	}

	// Multiple providers returned a usable match. Do they agree on
	// the canonical name?
	canonical := metadata.NormalizeKey(usable[0].Studio.Name)
	agree := usable[0]
	conflict := false
	for _, r := range usable[1:] {
		if metadata.NormalizeKey(r.Studio.Name) != canonical {
			conflict = true
			break
		}
		agree = r
	}
	if !conflict {
		return agree.Studio, agree.ProviderID, ""
	}

	// Real conflict between providers. Defer to the LLM if the
	// user opted in - the model's job is exactly this kind of
	// "same string, different upstream identity" disambiguation.
	if !j.input.UseLocalAIStudioProviderSelection {
		return nil, "", studioReasonProviderResultsConflict
	}
	if j.completer == nil {
		return nil, "", studioReasonLocalAIUnavailable
	}
	providers := make([]studioMetadataProvider, len(usable))
	for i, r := range usable {
		providers[i] = studioMetadataProvider{ID: r.ProviderID}
	}
	pickCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	choice, err := j.extractStudioProviderChoice(pickCtx, candidate, providers, sources)
	if err != nil {
		if ctx.Err() != nil {
			return nil, "", studioReasonCancelled
		}
		if errors.Is(err, errStudioProviderChoiceInvalid) {
			return nil, "", studioReasonLocalAIInvalid
		}
		return nil, "", studioReasonLocalAIUnavailable
	}
	if choice == nil {
		return nil, "", studioReasonLocalAIRejected
	}
	for _, r := range usable {
		if r.ProviderID == choice.ID {
			return r.Studio, r.ProviderID, ""
		}
	}
	return nil, "", studioReasonLocalAIInvalid
}

// resolveStudioCandidate resolves a single studio name candidate.
// It first checks the local library for an exact match, then
// fans out to every configured provider (Stash-box + studio
// scraper), then either adopts the single best result or defers
// to the LLM when providers disagree. It does not write anything
// to the scene - the caller does.
func (j *analyzeSceneMetadataJob) resolveStudioCandidate(ctx context.Context, sceneID int, candidate string, sources []metadata.Source) (studioResolution, error) {
	resolution := studioResolution{Candidate: candidate}

	if err := ctx.Err(); err != nil {
		resolution.Status = studioResolutionCancelled
		resolution.Reason = studioReasonCancelled
		return resolution, err
	}

	var library []studioIdentity
	err := j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		library, err = findExactStudioIdentities(ctx, j.repository.Studio, candidate)
		return err
	})
	if err != nil {
		if ctx.Err() != nil {
			resolution.Status = studioResolutionCancelled
			resolution.Reason = studioReasonCancelled
			return resolution, ctx.Err()
		}
		resolution.Status = studioResolutionUnverified
		resolution.Reason = studioReasonVerifierFailed
		return resolution, fmt.Errorf("finding exact studio identities for %q: %w", candidate, err)
	}

	matchingIDs := studioIdentityIDs(library)
	if len(library) == 1 {
		resolution.Status = studioResolutionExisting
		resolution.StudioID = library[0].ID
		resolution.MatchingIDs = matchingIDs
		resolution.Reason = studioReasonSingleExactLibraryMatch
		return resolution, nil
	}
	if j.input.DryRun {
		resolution.Status = studioResolutionDryRun
		resolution.MatchingIDs = matchingIDs
		resolution.Reason = studioReasonDryRun
		return resolution, nil
	}
	if len(library) > 1 {
		resolution.Status = studioResolutionAmbiguous
		resolution.MatchingIDs = matchingIDs
		resolution.Reason = studioReasonMultipleLibraryMatches
		return resolution, nil
	}

	if !j.studioPoolAvailable() {
		resolution.Status = studioResolutionUnverified
		resolution.Reason = studioReasonProviderUnavailable
		return resolution, nil
	}

	results, queryErr := j.queryAllStudioProviders(ctx, candidate)
	if queryErr != nil {
		if ctx.Err() != nil {
			resolution.Status = studioResolutionCancelled
			resolution.Reason = studioReasonCancelled
			return resolution, ctx.Err()
		}
	}

	scraped, providerID, errReason := j.reconcileStudioProviderResults(ctx, candidate, results, sources)
	if errReason != "" {
		resolution.Status = studioResolutionUnverified
		resolution.Reason = errReason
		return resolution, nil
	}
	resolution.ProviderID = providerID

	id, ok, created, reason := j.adoptScrapedStudio(ctx, candidate, library, scraped)
	if ok {
		resolution.StudioID = id
		resolution.MatchingIDs = append(matchingIDs, id)
		if created {
			resolution.Status = studioResolutionCreated
		} else {
			resolution.Status = studioResolutionExisting
		}
		resolution.Reason = reason
		return resolution, nil
	}
	resolution.Status = studioResolutionUnverified
	resolution.Reason = reason
	return resolution, nil
}

func (j *analyzeSceneMetadataJob) adoptScrapedStudio(ctx context.Context, candidate string, library []studioIdentity, scraped *models.ScrapedStudio) (int, bool, bool, string) {
	if scraped == nil {
		return 0, false, false, studioReasonProviderNoResult
	}
	if !studioCandidateMatchesScraped(candidate, scraped) {
		return 0, false, false, studioReasonProviderResultsConflict
	}
	if len(library) == 1 {
		return library[0].ID, true, false, studioReasonSingleExactLibraryMatch
	}
	if id, ok := matchScrapedStudioToLibrary(scraped, library); ok {
		return id, true, false, studioReasonSingleExactLibraryMatch
	}
	input := scraped.ToStudio("", map[string]bool{})
	if input.Name == "" {
		input.Name = candidate
	}
	if err := j.repository.WithTxn(ctx, func(ctx context.Context) error {
		return j.repository.Studio.Create(ctx, input)
	}); err != nil {
		logger.Warnf("[scene metadata] studio resolution candidate=%q create_error=%v", candidate, err)
		return 0, false, false, studioReasonVerifierFailed
	}
	var newID int
	if err := j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		created, err := j.repository.Studio.FindByName(ctx, input.Name, true)
		if err != nil || created == nil {
			return err
		}
		newID = created.ID
		return nil
	}); err != nil || newID == 0 {
		logger.Warnf("[scene metadata] studio resolution candidate=%q lookup_error=%v", candidate, err)
		return 0, false, false, studioReasonVerifierFailed
	}
	return newID, true, true, studioReasonCreatedAfterVerification
}

func studioCandidateMatchesScraped(candidate string, scraped *models.ScrapedStudio) bool {
	if scraped == nil {
		return false
	}
	candidateKey := metadata.NormalizeKey(candidate)
	if candidateKey == "" {
		return false
	}
	scrapedKey := metadata.NormalizeKey(scraped.Name)
	if scrapedKey == "" {
		return false
	}
	if candidateKey == scrapedKey {
		return true
	}
	candidateTokens := strings.Fields(candidateKey)
	scrapedTokens := strings.Fields(scrapedKey)
	if len(candidateTokens) == 0 || len(scrapedTokens) == 0 {
		return false
	}
	return allTokensContained(candidateTokens, scrapedTokens) ||
		allTokensContained(scrapedTokens, candidateTokens)
}

func allTokensContained(needles, haystack []string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if h == n {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func matchScrapedStudioToLibrary(scraped *models.ScrapedStudio, library []studioIdentity) (int, bool) {
	if scraped == nil || len(library) == 0 {
		return 0, false
	}
	scrapedURLs := make(map[string]struct{}, len(scraped.URLs))
	for _, raw := range scraped.URLs {
		norm, valid := normalizeProfileURL(raw)
		if !valid {
			continue
		}
		scrapedURLs[norm] = struct{}{}
	}
	if len(scrapedURLs) == 0 {
		return 0, false
	}
	matches := 0
	matchedID := 0
	for _, lib := range library {
		for _, raw := range lib.URLs {
			norm, valid := normalizeProfileURL(raw)
			if !valid {
				continue
			}
			if _, ok := scrapedURLs[norm]; ok {
				matches++
				matchedID = lib.ID
				break
			}
		}
	}
	if matches == 1 {
		return matchedID, true
	}
	return 0, false
}

func studioIdentityIDs(items []studioIdentity) []int {
	ret := make([]int, 0, len(items))
	for _, it := range items {
		ret = append(ret, it.ID)
	}
	return ret
}
