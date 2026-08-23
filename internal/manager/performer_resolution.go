package manager

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/performer"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stashapp/stash/pkg/stashbox"
)

type performerResolutionStatus string

const (
	performerResolutionExisting   performerResolutionStatus = "existing"
	performerResolutionProposed   performerResolutionStatus = "proposed"
	performerResolutionAmbiguous  performerResolutionStatus = "ambiguous"
	performerResolutionUnverified performerResolutionStatus = "unverified"
	performerResolutionCancelled  performerResolutionStatus = "cancelled"
)

const (
	performerReasonSingleExactLibraryMatch = "single_exact_library_match"
	performerReasonScraperIdentityMatch    = "scraper_identity_match"
	performerReasonVerifiedNewPerformer    = "verified_new_performer"
	performerReasonMultipleLibraryMatches  = "multiple_exact_library_matches"
	performerReasonScraperResultsConflict  = "scraper_results_conflict"
	performerReasonScraperNoExactResult    = "scraper_no_exact_result"
	performerReasonScraperUnavailable      = "scraper_unavailable"
	performerReasonVerifierFailed          = "verifier_failed"
	performerReasonLocalAIUnavailable      = "local_ai_unavailable"
	performerReasonLocalAIInvalid          = "local_ai_invalid"
	performerReasonCancelled               = "cancelled"
	performerReasonImplausibleName         = "implausible_name"
	performerReasonIgnoredMale             = "ignored_male"
)

type performerResolution struct {
	Candidate   string
	Status      performerResolutionStatus
	PerformerID int
	MatchingIDs []int
	ScraperIDs  []string
	Reason      string
	Proposed    *models.Performer
}

type performerIdentity struct {
	ID             int
	Name           string
	Aliases        []string
	Disambiguation string
	Birthdate      *models.Date
	URLs           []string
	Gender         models.GenderEnum
}

type scrapedPerformerIdentity struct {
	ScraperID      string
	Name           string
	StoredID       int
	RemoteSiteID   string
	Disambiguation string
	Birthdate      string
	URLs           []string
	Gender         models.GenderEnum
}

type performerVerifierFailure struct {
	ScraperID string
	Err       error
}

type performerStashBoxQuerier interface {
	QueryPerformer(context.Context, string) ([]*models.ScrapedPerformer, error)
}

type performerStashBoxVerifier struct {
	ID     string
	client performerStashBoxQuerier
}

func newPerformerStashBoxVerifier(box models.StashBox) performerStashBoxVerifier {
	return performerStashBoxVerifier{
		ID:     "stashbox:" + box.Endpoint,
		client: stashbox.NewClient(box),
	}
}

func parsePerformerGender(value string) (models.GenderEnum, bool) {
	gender := models.GenderEnum(strings.ToUpper(strings.TrimSpace(value)))
	if !gender.IsValid() {
		return "", false
	}
	return gender, true
}

func genderFromPointer(value *models.GenderEnum) models.GenderEnum {
	if value == nil {
		return ""
	}
	return *value
}

func (j *analyzeSceneMetadataJob) ignoreMale() bool {
	return j.input.IgnoreMalePerformers
}

func (j *analyzeSceneMetadataJob) skipMaleGender(g models.GenderEnum) bool {
	return j.ignoreMale() && g == models.GenderEnumMale
}

type pendingPerformerResolution struct {
	candidate  string
	library    []performerIdentity
	verified   []scrapedPerformerIdentity
	components [][]scrapedPerformerIdentity
	scraperIDs []string
	reason     string
}

func (j *analyzeSceneMetadataJob) resolvePerformerCandidates(ctx context.Context, sceneID int, candidates []string, sources []metadata.Source) ([]performerResolution, error) {
	ordered := deduplicatePerformerCandidates(candidates)
	resolutions := make([]performerResolution, 0, len(ordered))
	pending := make([]pendingPerformerResolution, 0)

	finalize := func(resolution performerResolution) {
		logPerformerResolution(sceneID, resolution)
		resolutions = append(resolutions, resolution)
	}

	for _, candidate := range ordered {
		if err := ctx.Err(); err != nil {
			finalize(performerResolution{Candidate: candidate, Status: performerResolutionCancelled, Reason: performerReasonCancelled})
			return resolutions, err
		}

		if metadata.ImplausiblePerformerName(candidate) {
			finalize(performerResolution{Candidate: candidate, Status: performerResolutionUnverified, Reason: performerReasonImplausibleName})
			continue
		}

		var library []performerIdentity
		err := j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
			var err error
			library, err = findExactPerformerIdentities(ctx, j.repository.Performer, candidate)
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				finalize(performerResolution{Candidate: candidate, Status: performerResolutionCancelled, Reason: performerReasonCancelled})
				return resolutions, ctx.Err()
			}
			finalize(performerResolution{Candidate: candidate, Status: performerResolutionUnverified, Reason: performerReasonVerifierFailed})
			return resolutions, fmt.Errorf("finding exact performer identities for %q: %w", candidate, err)
		}
		if kept := library[:0]; true {
			for _, identity := range library {
				if j.skipMaleGender(identity.Gender) {
					continue
				}
				kept = append(kept, identity)
			}
			library = kept
		}

		matchingIDs := performerIdentityIDs(library)
		if len(library) == 1 {
			finalize(performerResolution{
				Candidate: candidate, Status: performerResolutionExisting,
				PerformerID: library[0].ID, MatchingIDs: matchingIDs,
				Reason: performerReasonSingleExactLibraryMatch,
			})
			continue
		}
		if len(j.performerVerifierScrapers) == 0 && len(j.performerVerifierStashBoxes) == 0 {
			status, reason := performerResolutionUnverified, performerReasonScraperUnavailable
			if len(library) > 1 {
				status, reason = performerResolutionAmbiguous, performerReasonMultipleLibraryMatches
			}
			finalize(performerResolution{Candidate: candidate, Status: status, MatchingIDs: matchingIDs, Reason: reason})
			continue
		}

		verified, failures, verifyErr := j.scrapeVerifyPerformers(ctx, candidate)
		scraperIDs := append([]string(nil), j.lastVerifierScraperIDs...)
		for _, failure := range failures {
			logger.Warnf("[scene metadata] performer verifier candidate=%q verifier_id=%q error=%v", candidate, failure.ScraperID, failure.Err)
		}
		if verifyErr != nil {
			finalize(performerResolution{
				Candidate: candidate, Status: performerResolutionCancelled,
				MatchingIDs: matchingIDs, ScraperIDs: scraperIDs, Reason: performerReasonCancelled,
			})
			return resolutions, verifyErr
		}
		if kept := verified[:0]; true {
			for _, identity := range verified {
				if j.skipMaleGender(identity.Gender) {
					continue
				}
				kept = append(kept, identity)
			}
			verified = kept
		}
		if len(verified) == 0 {
			reason := performerReasonScraperNoExactResult
			if len(failures) == len(scraperIDs) && len(scraperIDs) > 0 {
				reason = performerReasonVerifierFailed
			}
			finalize(performerResolution{
				Candidate: candidate, Status: performerResolutionUnverified,
				MatchingIDs: matchingIDs, ScraperIDs: scraperIDs, Reason: reason,
			})
			continue
		}

		if id, ok := matchScrapedIdentityToLibrary(candidate, library, verified); ok {
			finalize(performerResolution{
				Candidate: candidate, Status: performerResolutionExisting, PerformerID: id,
				MatchingIDs: append(matchingIDs, id), ScraperIDs: scraperIDs,
				Reason: performerReasonScraperIdentityMatch,
			})
			continue
		}

		components := scrapedIdentityComponents(verified, library)
		if len(components) == 1 && len(library) == 0 {
			resolution, err := j.planVerifiedPerformer(ctx, candidate, components[0], nil)
			resolution.ScraperIDs = scraperIDs
			if err != nil {
				if ctx.Err() != nil {
					resolution = performerResolution{Candidate: candidate, Status: performerResolutionCancelled, ScraperIDs: scraperIDs, Reason: performerReasonCancelled}
					finalize(resolution)
					return resolutions, ctx.Err()
				}
				resolution = performerResolution{Candidate: candidate, Status: performerResolutionUnverified, ScraperIDs: scraperIDs, Reason: performerReasonVerifierFailed}
				finalize(resolution)
				logger.Warnf("[scene metadata] performer resolution candidate=%q transaction_error=%v", candidate, err)
				continue
			}
			finalize(resolution)
			continue
		}

		reason := performerReasonMultipleLibraryMatches
		if len(components) > 1 {
			reason = performerReasonScraperResultsConflict
		}
		entry := pendingPerformerResolution{
			candidate: candidate, library: library, verified: verified,
			components: components, scraperIDs: scraperIDs, reason: reason,
		}
		if !j.input.UseLocalAIContext {
			finalize(entry.ambiguousResolution())
			continue
		}
		pending = append(pending, entry)
	}

	if len(pending) == 0 {
		return resolutions, nil
	}

	evidence, contextErr := j.extractPerformerContext(ctx, pendingCandidateNames(pending), sources)
	if contextErr != nil {
		if ctx.Err() != nil {
			for _, entry := range pending {
				resolution := entry.ambiguousResolution()
				resolution.Status = performerResolutionCancelled
				resolution.Reason = performerReasonCancelled
				finalize(resolution)
			}
			return resolutions, ctx.Err()
		}
		reason := performerReasonLocalAIUnavailable
		if errors.Is(contextErr, errPerformerContextInvalid) {
			reason = performerReasonLocalAIInvalid
		}
		logger.Warnf("[scene metadata] local performer context fallback: %v", contextErr)
		for _, entry := range pending {
			resolution := entry.ambiguousResolution()
			resolution.Reason = reason
			finalize(resolution)
		}
		return resolutions, nil
	}

	for _, entry := range pending {
		component, id, ok := matchContextEvidence(entry.candidate, entry.library, entry.components, evidence)
		if !ok {
			finalize(entry.ambiguousResolution())
			continue
		}
		selected := entry.components[component]
		if id != 0 {
			finalize(performerResolution{
				Candidate: entry.candidate, Status: performerResolutionExisting, PerformerID: id,
				MatchingIDs: append(performerIdentityIDs(entry.library), id), ScraperIDs: entry.scraperIDs,
				Reason: performerReasonScraperIdentityMatch,
			})
			continue
		}
		resolution, err := j.planVerifiedPerformer(ctx, entry.candidate, selected, evidence)
		resolution.ScraperIDs = entry.scraperIDs
		if err != nil {
			if ctx.Err() != nil {
				resolution = performerResolution{Candidate: entry.candidate, Status: performerResolutionCancelled, ScraperIDs: entry.scraperIDs, Reason: performerReasonCancelled}
				finalize(resolution)
				return resolutions, ctx.Err()
			}
			resolution = performerResolution{Candidate: entry.candidate, Status: performerResolutionUnverified, ScraperIDs: entry.scraperIDs, Reason: performerReasonVerifierFailed}
			finalize(resolution)
			logger.Warnf("[scene metadata] performer resolution candidate=%q transaction_error=%v", entry.candidate, err)
			continue
		}
		finalize(resolution)
	}

	return resolutions, nil
}

func (p pendingPerformerResolution) ambiguousResolution() performerResolution {
	return performerResolution{
		Candidate: p.candidate, Status: performerResolutionAmbiguous,
		MatchingIDs: performerIdentityIDs(p.library), ScraperIDs: p.scraperIDs, Reason: p.reason,
	}
}

func pendingCandidateNames(pending []pendingPerformerResolution) []string {
	ret := make([]string, 0, len(pending))
	for _, entry := range pending {
		ret = append(ret, entry.candidate)
	}
	return ret
}

func deduplicatePerformerCandidates(candidates []string) []string {
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

func findExactPerformerIdentities(ctx context.Context, r models.PerformerReader, candidate string) ([]performerIdentity, error) {
	var found []*models.Performer
	if isASCII(candidate) {
		byName, err := r.FindByNames(ctx, []string{candidate}, true)
		if err != nil {
			return nil, err
		}
		byAlias, err := performer.ByAlias(ctx, r, candidate)
		if err != nil {
			return nil, err
		}
		found = append(byName, byAlias...)
	} else {
		all, err := r.All(ctx)
		if err != nil {
			return nil, err
		}
		found = all
	}

	candidateKey := metadata.NormalizeKey(candidate)
	byID := make(map[int]performerIdentity, len(found))
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
		var birthdate *models.Date
		if current.Birthdate != nil {
			value := *current.Birthdate
			birthdate = &value
		}
		byID[current.ID] = performerIdentity{
			ID: current.ID, Name: current.Name, Aliases: aliases,
			Disambiguation: current.Disambiguation, Birthdate: birthdate,
			URLs: append([]string(nil), current.URLs.List()...),
			Gender: genderFromPointer(current.Gender),
		}
	}
	ret := make([]performerIdentity, 0, len(byID))
	for _, identity := range byID {
		ret = append(ret, identity)
	}
	sort.Slice(ret, func(i, k int) bool { return ret[i].ID < ret[k].ID })
	return ret, nil
}

func isASCII(value string) bool {
	for _, r := range value {
		if r > 127 {
			return false
		}
	}
	return true
}

func containsNormalized(values []string, key string) bool {
	for _, value := range values {
		if metadata.NormalizeKey(value) == key {
			return true
		}
	}
	return false
}

func normalizeProfileURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Fragment = ""
	if parsed.Path != "/" {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	}
	return parsed.String(), true
}

func matchScrapedIdentityToLibrary(candidate string, library []performerIdentity, verified []scrapedPerformerIdentity) (int, bool) {
	libraryByID := make(map[int]performerIdentity, len(library))
	for _, identity := range library {
		libraryByID[identity.ID] = identity
	}

	matchedID := 0
	acceptEvidence := func(matches map[int]struct{}) bool {
		if len(matches) == 0 {
			return true
		}
		id, ok := singleID(matches)
		if !ok || matchedID != 0 && matchedID != id {
			return false
		}
		matchedID = id
		return true
	}
	stored := make(map[int]struct{})
	for _, identity := range verified {
		if _, exists := libraryByID[identity.StoredID]; identity.StoredID > 0 && exists {
			stored[identity.StoredID] = struct{}{}
		}
	}
	if !acceptEvidence(stored) {
		return 0, false
	}

	scrapedURLs := normalizedURLSetFromScraped(verified)
	urlMatches := make(map[int]struct{})
	for _, identity := range library {
		for _, raw := range identity.URLs {
			normalized, valid := normalizeProfileURL(raw)
			if valid {
				if _, exists := scrapedURLs[normalized]; exists {
					urlMatches[identity.ID] = struct{}{}
				}
			}
		}
	}
	if !acceptEvidence(urlMatches) {
		return 0, false
	}

	candidateKey := metadata.NormalizeKey(candidate)
	disambiguations := make(map[string]struct{})
	for _, identity := range verified {
		value := metadata.NormalizeKey(identity.Disambiguation)
		if metadata.NormalizeKey(identity.Name) == candidateKey && value != "" {
			disambiguations[value] = struct{}{}
		}
	}
	disambiguationMatches := make(map[int]struct{})
	for _, identity := range library {
		if metadata.NormalizeKey(identity.Name) != candidateKey {
			continue
		}
		if _, exists := disambiguations[metadata.NormalizeKey(identity.Disambiguation)]; exists && metadata.NormalizeKey(identity.Disambiguation) != "" {
			disambiguationMatches[identity.ID] = struct{}{}
		}
	}
	if !acceptEvidence(disambiguationMatches) {
		return 0, false
	}

	birthdates := make(map[string]struct{})
	for _, identity := range verified {
		if metadata.NormalizeKey(identity.Name) != candidateKey {
			continue
		}
		if parsed, err := models.ParseDate(strings.TrimSpace(identity.Birthdate)); err == nil {
			birthdates[parsed.String()] = struct{}{}
		}
	}
	birthdateMatches := make(map[int]struct{})
	for _, identity := range library {
		if metadata.NormalizeKey(identity.Name) != candidateKey || identity.Birthdate == nil {
			continue
		}
		if _, exists := birthdates[identity.Birthdate.String()]; exists {
			birthdateMatches[identity.ID] = struct{}{}
		}
	}
	if !acceptEvidence(birthdateMatches) || matchedID == 0 {
		return 0, false
	}
	return matchedID, true
}

func scrapedIdentityComponents(verified []scrapedPerformerIdentity, library []performerIdentity) [][]scrapedPerformerIdentity {
	if len(verified) == 0 {
		return nil
	}
	parents := make([]int, len(verified))
	for i := range parents {
		parents[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		if parents[i] != i {
			parents[i] = root(parents[i])
		}
		return parents[i]
	}
	join := func(a, b int) {
		ra, rb := root(a), root(b)
		if ra != rb {
			parents[rb] = ra
		}
	}
	validStored := make(map[int]struct{}, len(library))
	for _, identity := range library {
		validStored[identity.ID] = struct{}{}
	}
	for i := range verified {
		for k := i + 1; k < len(verified); k++ {
			if sameStableScrapedIdentity(verified[i], verified[k], validStored) {
				join(i, k)
			}
		}
	}
	componentIndexes := make(map[int]int)
	ret := make([][]scrapedPerformerIdentity, 0, len(verified))
	for i, identity := range verified {
		r := root(i)
		index, exists := componentIndexes[r]
		if !exists {
			index = len(ret)
			componentIndexes[r] = index
			ret = append(ret, nil)
		}
		ret[index] = append(ret[index], identity)
	}
	return ret
}

func sameStableScrapedIdentity(left, right scrapedPerformerIdentity, validStored map[int]struct{}) bool {
	if left.StoredID > 0 && left.StoredID == right.StoredID {
		if _, valid := validStored[left.StoredID]; valid {
			return true
		}
	}
	if left.ScraperID != "" && left.ScraperID == right.ScraperID && left.RemoteSiteID != "" && left.RemoteSiteID == right.RemoteSiteID {
		return true
	}
	leftURLs := normalizedURLSet(left.URLs)
	for _, raw := range right.URLs {
		if normalized, valid := normalizeProfileURL(raw); valid {
			if _, exists := leftURLs[normalized]; exists {
				return true
			}
		}
	}
	return false
}

func normalizedURLSetFromScraped(identities []scrapedPerformerIdentity) map[string]struct{} {
	ret := make(map[string]struct{})
	for _, identity := range identities {
		for normalized := range normalizedURLSet(identity.URLs) {
			ret[normalized] = struct{}{}
		}
	}
	return ret
}

func normalizedURLSet(values []string) map[string]struct{} {
	ret := make(map[string]struct{}, len(values))
	for _, raw := range values {
		if normalized, valid := normalizeProfileURL(raw); valid {
			ret[normalized] = struct{}{}
		}
	}
	return ret
}

type performerContextMatch struct {
	component   int
	performerID int
}

func matchContextEvidence(candidate string, library []performerIdentity, components [][]scrapedPerformerIdentity, evidence []performerContextEvidence) (int, int, bool) {
	matches := make(map[performerContextMatch]struct{})
	candidateKey := metadata.NormalizeKey(candidate)
	for _, clue := range evidence {
		if metadata.NormalizeKey(clue.Name) != candidateKey {
			continue
		}
		collectContextFieldMatches(matches, components, library,
			func(scraped scrapedPerformerIdentity) bool {
				return clue.Disambiguation != "" && metadata.NormalizeKey(scraped.Disambiguation) == metadata.NormalizeKey(clue.Disambiguation)
			},
			func(current performerIdentity) bool {
				return metadata.NormalizeKey(current.Name) == candidateKey && clue.Disambiguation != "" && metadata.NormalizeKey(current.Disambiguation) == metadata.NormalizeKey(clue.Disambiguation)
			},
		)
		collectContextFieldMatches(matches, components, library,
			func(scraped scrapedPerformerIdentity) bool { return datesEqual(scraped.Birthdate, clue.Birthdate) },
			func(current performerIdentity) bool {
				return metadata.NormalizeKey(current.Name) == candidateKey && current.Birthdate != nil && dateMatches(current.Birthdate, clue.Birthdate)
			},
		)
		clueURL, clueURLValid := normalizeProfileURL(clue.ProfileURL)
		collectContextFieldMatches(matches, components, library,
			func(scraped scrapedPerformerIdentity) bool {
				return clueURLValid && urlSetContains(normalizedURLSet(scraped.URLs), clueURL)
			},
			func(current performerIdentity) bool {
				return metadata.NormalizeKey(current.Name) == candidateKey && clueURLValid && urlSetContains(normalizedURLSet(current.URLs), clueURL)
			},
		)
	}
	if len(matches) != 1 {
		return 0, 0, false
	}
	for value := range matches {
		return value.component, value.performerID, true
	}
	return 0, 0, false
}

func collectContextFieldMatches(matches map[performerContextMatch]struct{}, components [][]scrapedPerformerIdentity, library []performerIdentity, scrapedMatch func(scrapedPerformerIdentity) bool, libraryMatch func(performerIdentity) bool) {
	componentIDs := make([]int, 0, 1)
	for index, component := range components {
		matched := false
		for _, identity := range component {
			if scrapedMatch(identity) {
				matched = true
				break
			}
		}
		if matched {
			componentIDs = append(componentIDs, index)
		}
	}
	performerIDs := make([]int, 0, 1)
	for _, identity := range library {
		if libraryMatch(identity) {
			performerIDs = append(performerIDs, identity.ID)
		}
	}
	if len(componentIDs) == 1 && len(performerIDs) == 1 {
		matches[performerContextMatch{component: componentIDs[0], performerID: performerIDs[0]}] = struct{}{}
	}
}

func datesEqual(left, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	leftDate, leftErr := models.ParseDate(strings.TrimSpace(left))
	rightDate, rightErr := models.ParseDate(strings.TrimSpace(right))
	return leftErr == nil && rightErr == nil && leftDate.String() == rightDate.String()
}

func dateMatches(current *models.Date, value string) bool {
	parsed, err := models.ParseDate(strings.TrimSpace(value))
	return err == nil && current.String() == parsed.String()
}

func urlSetContains(values map[string]struct{}, value string) bool {
	_, exists := values[value]
	return exists
}

func singleID(ids map[int]struct{}) (int, bool) {
	if len(ids) != 1 {
		return 0, false
	}
	for id := range ids {
		return id, true
	}
	return 0, false
}

func performerIdentityIDs(identities []performerIdentity) []int {
	ret := make([]int, 0, len(identities))
	for _, identity := range identities {
		ret = append(ret, identity.ID)
	}
	return ret
}

func (j *analyzeSceneMetadataJob) scrapeVerifyPerformers(ctx context.Context, candidate string) ([]scrapedPerformerIdentity, []performerVerifierFailure, error) {
	j.lastVerifierScraperIDs = nil
	verified := make([]scrapedPerformerIdentity, 0)
	failures := make([]performerVerifierFailure, 0)
	for _, selected := range j.performerVerifierScrapers {
		if err := ctx.Err(); err != nil {
			return verified, failures, err
		}
		j.lastVerifierScraperIDs = append(j.lastVerifierScraperIDs, selected.ID)
		content, err := j.scraperCache.ScrapeName(ctx, selected.ID, candidate, scraper.ScrapeContentTypePerformer)
		if err != nil {
			failures = append(failures, performerVerifierFailure{ScraperID: selected.ID, Err: err})
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				if ctx.Err() != nil {
					return verified, failures, ctx.Err()
				}
				return verified, failures, err
			}
			continue
		}
		for _, item := range content {
			var scraped *models.ScrapedPerformer
			switch value := item.(type) {
			case *models.ScrapedPerformer:
				scraped = value
			case models.ScrapedPerformer:
				copy := value
				scraped = &copy
			}
			if scraped == nil || scraped.Name == nil || !strings.EqualFold(strings.TrimSpace(*scraped.Name), strings.TrimSpace(candidate)) {
				continue
			}
			verified = append(verified, scrapedPerformerIdentityFromModel(selected.ID, scraped))
		}
	}
	for _, selected := range j.performerVerifierStashBoxes {
		if err := ctx.Err(); err != nil {
			return verified, failures, err
		}
		j.lastVerifierScraperIDs = append(j.lastVerifierScraperIDs, selected.ID)
		content, err := selected.client.QueryPerformer(ctx, candidate)
		if err != nil {
			failures = append(failures, performerVerifierFailure{ScraperID: selected.ID, Err: err})
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
				if ctx.Err() != nil {
					return verified, failures, ctx.Err()
				}
				return verified, failures, err
			}
			continue
		}
		for _, scraped := range content {
			if scraped == nil || scraped.Name == nil || !strings.EqualFold(strings.TrimSpace(*scraped.Name), strings.TrimSpace(candidate)) {
				continue
			}
			verified = append(verified, scrapedPerformerIdentityFromModel(selected.ID, scraped))
		}
	}
	return verified, failures, nil
}

func scrapedPerformerIdentityFromModel(scraperID string, scraped *models.ScrapedPerformer) scrapedPerformerIdentity {
	identity := scrapedPerformerIdentity{ScraperID: scraperID}
	if scraped.Name != nil {
		identity.Name = strings.TrimSpace(*scraped.Name)
	}
	if scraped.StoredID != nil {
		identity.StoredID, _ = strconv.Atoi(strings.TrimSpace(*scraped.StoredID))
		if identity.StoredID < 1 {
			identity.StoredID = 0
		}
	}
	if scraped.RemoteSiteID != nil {
		identity.RemoteSiteID = strings.TrimSpace(*scraped.RemoteSiteID)
	}
	if scraped.Disambiguation != nil {
		identity.Disambiguation = strings.TrimSpace(*scraped.Disambiguation)
	}
	if scraped.Birthdate != nil {
		identity.Birthdate = strings.TrimSpace(*scraped.Birthdate)
	}
	identity.URLs = append(identity.URLs, scraped.URLs...)
	for _, legacy := range []*string{scraped.URL, scraped.Twitter, scraped.Instagram} {
		if legacy != nil && strings.TrimSpace(*legacy) != "" {
			identity.URLs = append(identity.URLs, strings.TrimSpace(*legacy))
		}
	}
	if scraped.Gender != nil {
		if gender, ok := parsePerformerGender(*scraped.Gender); ok {
			identity.Gender = gender
		}
	}
	return identity
}

func (j *analyzeSceneMetadataJob) planVerifiedPerformer(ctx context.Context, candidate string, verified []scrapedPerformerIdentity, evidence []performerContextEvidence) (performerResolution, error) {
	resolution := performerResolution{Candidate: candidate}
	var fresh []performerIdentity
	if err := j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		fresh, err = findExactPerformerIdentities(ctx, j.repository.Performer, candidate)
		return err
	}); err != nil {
		return resolution, err
	}
	if j.ignoreMale() {
		// Drop male library matches before computing MatchingIDs so an
		// ignore-male policy cannot be silently bypassed by a single
		// remaining exact-match after filtering.
		kept := fresh[:0]
		for _, identity := range fresh {
			if identity.Gender == models.GenderEnumMale {
				continue
			}
			kept = append(kept, identity)
		}
		fresh = kept
	}
	resolution.MatchingIDs = performerIdentityIDs(fresh)
	if len(fresh) == 1 {
		resolution.Status = performerResolutionExisting
		resolution.PerformerID = fresh[0].ID
		resolution.Reason = performerReasonSingleExactLibraryMatch
		return resolution, nil
	}
	if id, ok := matchScrapedIdentityToLibrary(candidate, fresh, verified); ok {
		resolution.Status = performerResolutionExisting
		resolution.PerformerID = id
		resolution.MatchingIDs = append(resolution.MatchingIDs, id)
		resolution.Reason = performerReasonScraperIdentityMatch
		return resolution, nil
	}
	components := scrapedIdentityComponents(verified, fresh)
	if len(fresh) > 1 {
		if _, id, ok := matchContextEvidence(candidate, fresh, components, evidence); ok && id != 0 {
			resolution.Status = performerResolutionExisting
			resolution.PerformerID = id
			resolution.MatchingIDs = append(resolution.MatchingIDs, id)
			resolution.Reason = performerReasonScraperIdentityMatch
			return resolution, nil
		}
		resolution.Status = performerResolutionAmbiguous
		resolution.Reason = performerReasonMultipleLibraryMatches
		return resolution, nil
	}
	if len(components) != 1 || len(components[0]) == 0 {
		resolution.Status = performerResolutionAmbiguous
		resolution.Reason = performerReasonScraperResultsConflict
		return resolution, nil
	}

	selected := components[0][0]
	if j.skipMaleGender(selected.Gender) {
		resolution.Status = performerResolutionUnverified
		resolution.Reason = performerReasonIgnoredMale
		return resolution, nil
	}
	newPerformer := models.NewPerformer()
	newPerformer.Name = strings.TrimSpace(selected.Name)
	newPerformer.Disambiguation = strings.TrimSpace(selected.Disambiguation)
	if selected.Gender.IsValid() {
		gender := selected.Gender
		newPerformer.Gender = &gender
	}
	if parsed, err := models.ParseDate(strings.TrimSpace(selected.Birthdate)); err == nil {
		newPerformer.Birthdate = &parsed
	}
	urls := make([]string, 0, len(selected.URLs))
	seenURLs := make(map[string]struct{}, len(selected.URLs))
	for _, raw := range selected.URLs {
		normalized, valid := normalizeProfileURL(raw)
		if !valid {
			continue
		}
		if _, exists := seenURLs[normalized]; exists {
			continue
		}
		seenURLs[normalized] = struct{}{}
		urls = append(urls, normalized)
	}
	newPerformer.URLs = models.NewRelatedStrings(urls)
	resolution.Status = performerResolutionProposed
	resolution.Proposed = &newPerformer
	resolution.Reason = performerReasonVerifiedNewPerformer
	return resolution, nil
}

func logPerformerResolution(sceneID int, resolution performerResolution) {
	matchingIDs := sortedUniqueInts(append([]int(nil), resolution.MatchingIDs...))
	if resolution.PerformerID != 0 {
		matchingIDs = sortedUniqueInts(append(matchingIDs, resolution.PerformerID))
	}
	scraperIDs := sortedUniqueStrings(append([]string(nil), resolution.ScraperIDs...))
	logger.Infof("[scene metadata] performer_resolution scene_id=%d candidate=%q status=%q matching_ids=%v scraper_ids=%v reason=%q",
		sceneID, resolution.Candidate, resolution.Status, matchingIDs, scraperIDs, resolution.Reason)
}

func sortedUniqueInts(values []int) []int {
	sort.Ints(values)
	ret := values[:0]
	for _, value := range values {
		if value == 0 || len(ret) > 0 && ret[len(ret)-1] == value {
			continue
		}
		ret = append(ret, value)
	}
	return ret
}

func sortedUniqueStrings(values []string) []string {
	sort.Strings(values)
	ret := values[:0]
	for _, value := range values {
		if value == "" || len(ret) > 0 && ret[len(ret)-1] == value {
			continue
		}
		ret = append(ret, value)
	}
	return ret
}
