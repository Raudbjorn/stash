package manager

import (
	"context"
	"fmt"
	"github.com/stashapp/stash/pkg/ffmpeg"
	"path/filepath"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
)

const (
	defaultPerformerConfidenceThreshold = 0.6
	defaultDateConfidenceThreshold      = 0.6

	// minCandidatePlausibility gates a regex-extracted candidate before it's
	// even worth spending a scraper lookup on.
	minCandidatePlausibility = 0.3
)

type AnalyzeSceneMetadataInput struct {
	// SceneIDs to process. If empty, all scenes are processed.
	SceneIDs []string `json:"sceneIDs"`
	// DryRun computes and logs proposed changes without writing anything.
	DryRun bool `json:"dryRun"`
	// PerformerVerifierScraperIDs selects configured performer name scrapers.
	// PerformerVerifierStashBoxEndpoints selects configured Stash-box APIs.
	// When both are empty, network verification is disabled.
	PerformerVerifierScraperIDs        []string `json:"performerVerifierScraperIDs"`
	PerformerVerifierStashBoxEndpoints []string `json:"performerVerifierStashBoxEndpoints"`
	// PerformerConfidenceThreshold is the minimum plausibility score for a
	// newly-discovered (not-yet-in-library) performer name to be looked up
	// via scrapers and, if confirmed, created. Defaults to 0.6.
	PerformerConfidenceThreshold *float64 `json:"performerConfidenceThreshold"`
	// DateConfidenceThreshold is the minimum confidence for a resolved date
	// to be written to a scene that doesn't already have one. Defaults to 0.6.
	DateConfidenceThreshold *float64 `json:"dateConfidenceThreshold"`
	// OverwriteExistingDate allows overwriting a scene's existing date, but
	// only ever from a high-confidence (EXIF/container-timestamp-tier)
	// signal - never from a text-derived guess. Defaults to false.
	OverwriteExistingDate bool `json:"overwriteExistingDate"`
	// UseDetails additionally analyzes the scene's details/description text.
	// Off by default since it's often noisy marketing copy.
	// UseLocalAIContext optionally uses the active local llama.cpp provider to
	// narrow ambiguous exact-name identities. It is off by default.
	UseLocalAIContext bool `json:"useLocalAIContext"`
	UseDetails        bool `json:"useDetails"`
	// StudioVerifierScraperIDs selects configured studio-name scrapers.
	// StudioVerifierStashBoxEndpoints selects configured Stash-box APIs that
	// can look up studios by name. When both are empty, network verification
	// of newly-discovered studio names is disabled.
	StudioVerifierScraperIDs        []string `json:"studioVerifierScraperIDs"`
	StudioVerifierStashBoxEndpoints []string `json:"studioVerifierStashBoxEndpoints"`
	// UseLocalAIStudioProviderSelection optionally uses the active local
	// llama.cpp provider to choose which configured studio metadata
	// provider (scraper or Stash-box) should be queried for the scene's
	// candidate studio name, based on the source strings (filename, title,
	// NFO, container metadata). Off by default.
	UseLocalAIStudioProviderSelection bool `json:"useLocalAIStudioProviderSelection"`
	// OverwriteExistingTitle allows replacing a user-authored scene title.
	// Without it, only empty or filename-derived default titles are changed.
	OverwriteExistingTitle bool `json:"overwriteExistingTitle"`
}

func (s *Manager) AnalyzeSceneMetadata(ctx context.Context, input AnalyzeSceneMetadataInput) int {
	j := &analyzeSceneMetadataJob{
		repository:           s.Repository,
		input:                input,
		ffprobe:              s.FFProbe,
		completer:            sceneMetadataCompleter(s.AIServer, s.Config),
		configuredStashBoxes: s.Config.GetStashBoxes(),
	}

	return s.JobManager.Add(ctx, "Analyzing scene metadata...", j)
}

type performerScraperCache interface {
	ListScrapers([]scraper.ScrapeContentType) []*scraper.Scraper
	ScrapeName(context.Context, string, string, scraper.ScrapeContentType) ([]scraper.ScrapedContent, error)
}

type analyzeSceneMetadataJob struct {
	repository                  models.Repository
	input                       AnalyzeSceneMetadataInput
	scraperCache                performerScraperCache
	scenePerformerLookup        scenePerformerLookup
	ffprobe                     *ffmpeg.FFProbe
	performerRecords            []metadata.NamedAliases
	studioRecords               []metadata.NamedAliases
	groupRecords                []metadata.NamedAliases
	performerVerifierScrapers   []*scraper.Scraper
	studioVerifierScrapers      []*scraper.Scraper
	useBuiltinStudioURLScraper  bool
	configuredStashBoxes        []*models.StashBox
	performerVerifierStashBoxes []performerStashBoxVerifier
	studioVerifierStashBoxes    []studioStashBoxVerifier
	completer                   structuredTextCompleter
	lastVerifierScraperIDs      []string
}

func (j *analyzeSceneMetadataJob) Execute(ctx context.Context, progress *job.Progress) error {
	r := j.repository
	if j.scraperCache == nil {
		j.scraperCache = instance.ScraperCache
	}
	j.resolvePerformerVerifierScrapers()
	j.resolvePerformerVerifierStashBoxes()
	j.resolveStudioVerifierScrapers()
	j.resolveStudioVerifierStashBoxes()

	sceneIDs, err := stringslice.StringSliceToIntSlice(j.input.SceneIDs)
	if err != nil {
		return fmt.Errorf("parsing scene ids: %w", err)
	}

	var scenes []*models.Scene
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		if len(sceneIDs) > 0 {
			scenes, err = r.Scene.FindMany(ctx, sceneIDs)
			return err
		}

		scenes, err = r.Scene.All(ctx)
		return err
	}); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("finding scenes: %w", err)
	}
	if err := j.loadLibraryRecords(ctx); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("loading metadata entity indexes: %w", err)
	}

	progress.SetTotal(len(scenes))

	for _, sc := range scenes {
		if job.IsCancelled(ctx) {
			return nil
		}

		var processErr error
		progress.ExecuteTask(fmt.Sprintf("Analyzing metadata for %s", sc.GetTitle()), func() {
			processErr = j.processScene(ctx, sc)
		})
		if processErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Errorf("[scene metadata] error processing scene %d: %v", sc.ID, processErr)
		}

		progress.Increment()
	}

	return nil
}
func (j *analyzeSceneMetadataJob) loadLibraryRecords(ctx context.Context) error {
	return j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		performers, err := j.repository.Performer.All(ctx)
		if err != nil {
			return err
		}
		j.performerRecords = make([]metadata.NamedAliases, 0, len(performers))
		for _, performer := range performers {
			if err := performer.LoadAliases(ctx, j.repository.Performer); err != nil {
				return err
			}
			j.performerRecords = append(j.performerRecords, metadata.NamedAliases{
				ID: performer.ID, Name: performer.Name, Aliases: performer.Aliases.List(),
			})
		}

		studios, err := j.repository.Studio.All(ctx)
		if err != nil {
			return err
		}
		j.studioRecords = make([]metadata.NamedAliases, 0, len(studios))
		for _, studio := range studios {
			if err := studio.LoadAliases(ctx, j.repository.Studio); err != nil {
				return err
			}
			j.studioRecords = append(j.studioRecords, metadata.NamedAliases{
				ID: studio.ID, Name: studio.Name, Aliases: studio.Aliases.List(),
			})
		}

		groups, err := j.repository.Group.All(ctx)
		if err != nil {
			return err
		}
		j.groupRecords = make([]metadata.NamedAliases, 0, len(groups))
		for _, group := range groups {
			if err := group.LoadAliases(ctx, j.repository.Group); err != nil {
				return err
			}
			j.groupRecords = append(j.groupRecords, metadata.NamedAliases{
				ID: group.ID, Name: group.Name, Aliases: group.Aliases.List(),
			})
		}
		return nil
	})
}

func (j *analyzeSceneMetadataJob) resolvePerformerVerifierScrapers() {
	j.performerVerifierScrapers = nil
	if j.scraperCache == nil {
		return
	}

	available := j.scraperCache.ListScrapers([]scraper.ScrapeContentType{scraper.ScrapeContentTypePerformer})
	byID := make(map[string]*scraper.Scraper, len(available))
	for _, s := range available {
		if s != nil {
			byID[s.ID] = s
		}
	}

	seen := make(map[string]struct{}, len(j.input.PerformerVerifierScraperIDs))
	for _, id := range j.input.PerformerVerifierScraperIDs {
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}

		s, found := byID[id]
		if !found {
			logger.Warnf("[scene metadata] performer verifier scraper %q is unavailable; skipping", id)
			continue
		}
		if !supportsPerformerNameScrape(s) {
			logger.Warnf("[scene metadata] performer verifier scraper %q does not support name searches; skipping", id)
			continue
		}

		j.performerVerifierScrapers = append(j.performerVerifierScrapers, s)
	}
}

func (j *analyzeSceneMetadataJob) resolvePerformerVerifierStashBoxes() {
	j.performerVerifierStashBoxes = nil
	byEndpoint := make(map[string]*models.StashBox, len(j.configuredStashBoxes))
	for _, box := range j.configuredStashBoxes {
		if box != nil && strings.TrimSpace(box.Endpoint) != "" {
			byEndpoint[box.Endpoint] = box
		}
	}

	seen := make(map[string]struct{}, len(j.input.PerformerVerifierStashBoxEndpoints))
	for _, endpoint := range j.input.PerformerVerifierStashBoxEndpoints {
		endpoint = strings.TrimSpace(endpoint)
		if _, duplicate := seen[endpoint]; duplicate {
			continue
		}
		seen[endpoint] = struct{}{}
		box, found := byEndpoint[endpoint]
		if !found {
			logger.Warnf("[scene metadata] performer verifier Stash-box endpoint %q is unavailable; skipping", endpoint)
			continue
		}
		j.performerVerifierStashBoxes = append(j.performerVerifierStashBoxes, newPerformerStashBoxVerifier(*box))
	}
}

func supportsPerformerNameScrape(s *scraper.Scraper) bool {
	if s == nil || s.Performer == nil {
		return false
	}
	for _, scrapeType := range s.Performer.SupportedScrapes {
		if scrapeType == scraper.ScrapeTypeName {
			return true
		}
	}
	return false
}

func (j *analyzeSceneMetadataJob) performerConfidenceThreshold() float64 {
	if j.input.PerformerConfidenceThreshold != nil {
		return *j.input.PerformerConfidenceThreshold
	}
	return defaultPerformerConfidenceThreshold
}

func (j *analyzeSceneMetadataJob) performerLookupThreshold() float64 {
	return max(minCandidatePlausibility, j.performerConfidenceThreshold())
}

func (j *analyzeSceneMetadataJob) dateConfidenceThreshold() float64 {
	if j.input.DateConfidenceThreshold != nil {
		return *j.input.DateConfidenceThreshold
	}
	return defaultDateConfidenceThreshold
}

func (j *analyzeSceneMetadataJob) processScene(ctx context.Context, sc *models.Scene) error {
	r := j.repository
	var (
		existingPerformerIDs []int
		existingGroups       []models.GroupsScenes
		primary              *models.VideoFile
	)
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		if err := sc.LoadPerformerIDs(ctx, r.Scene); err != nil {
			return err
		}
		existingPerformerIDs = append([]int(nil), sc.PerformerIDs.List()...)
		if err := sc.LoadStashIDs(ctx, r.Scene); err != nil {
			return err
		}
		if err := sc.LoadGroups(ctx, r.Scene); err != nil {
			return err
		}
		existingGroups = append([]models.GroupsScenes(nil), sc.Groups.List()...)
		if err := sc.LoadFiles(ctx, r.Scene); err != nil {
			return err
		}
		primary = sc.Files.Primary()
		return nil
	}); err != nil {
		return fmt.Errorf("loading scene: %w", err)
	}

	fileDirty := false
	if primary != nil && !primary.MetadataProbed && j.ffprobe != nil {
		if probed, err := j.ffprobe.NewVideoFileContext(ctx, primary.Path); err == nil {
			primary.Title = probed.Title
			primary.Comment = probed.Comment
			primary.Encoder = probed.Encoder
			primary.Tags = probed.Tags
			if !probed.CreationTime.IsZero() {
				primary.CreationTime = probed.CreationTime
			}
			primary.MetadataProbed = true
			fileDirty = true
		} else if ctx.Err() != nil {
			return ctx.Err()
		} else {
			logger.Debugf("[scene metadata] scene %d: container metadata unavailable", sc.ID)
		}
	}

	var nfo *metadata.NFOData
	if primary != nil {
		if parsed, err := metadata.ReadAdjacentNFO(primary.Path); err == nil {
			nfo = parsed
		} else {
			logger.Debugf("[scene metadata] scene %d: adjacent NFO unavailable", sc.ID)
		}
	}

	filenameStem := ""
	container := metadata.ContainerMetadata{}
	sanityBound := time.Now()
	var dateSignals []metadata.DateSignal
	if primary != nil {
		filenameStem = strings.TrimSuffix(filepath.Base(primary.Path), filepath.Ext(primary.Path))
		container = metadata.ContainerMetadata{
			Title: primary.Title, Comment: primary.Comment, Encoder: primary.Encoder, Tags: primary.Tags,
		}
		if !primary.ModTime.IsZero() {
			sanityBound = primary.ModTime
		}
		if !primary.CreationTime.IsZero() {
			dateSignals = append(dateSignals, metadata.DateSignal{
				Date: primary.CreationTime, Priority: metadata.DatePriorityVideoCreation, Source: "video creation time",
			})
		}
	}
	sources := metadata.CollectSources(metadata.SourceInputs{
		FilenameStem: filenameStem, SceneTitle: sc.Title, Details: sc.Details,
		UseDetails: j.input.UseDetails, NFO: nfo, Container: container,
	})
	authoritativePerformerIDs, hasAuthoritativePerformers := j.authoritativeScenePerformerIDs(ctx, sc)
	if len(sources) == 0 && !hasAuthoritativePerformers {
		return nil
	}

	var analysis metadata.Analysis
	if len(sources) > 0 {
		var exactSpans []metadata.Span
		for _, source := range sources {
			exactSpans = append(exactSpans, metadata.FindExactNamedSpans(source, j.performerRecords, metadata.EntityLabelPerformer)...)
			exactSpans = append(exactSpans, metadata.FindExactNamedSpans(source, j.studioRecords, metadata.EntityLabelStudio)...)
			exactSpans = append(exactSpans, metadata.FindExactNamedSpans(source, j.groupRecords, metadata.EntityLabelMovie)...)
		}
		var err error
		analysis, err = (metadata.Analyzer{}).Analyze(ctx, metadata.Inputs{
			Sources: sources, EntityExtractor: getSceneMetadataEntityExtractor(),
			AdditionalSpans: exactSpans, DateSignals: dateSignals, SanityBound: sanityBound,
		})
		if err != nil {
			return fmt.Errorf("analyzing typed metadata: %w", err)
		}
	}

	resolvedPerformerIDs := authoritativePerformerIDs
	if !hasAuthoritativePerformers {
		var performerCandidates []string
		for _, candidate := range analysis.PerformerCandidates {
			if candidate.ExistingEntityID != nil || candidate.Confidence >= j.performerLookupThreshold() {
				performerCandidates = append(performerCandidates, candidate.Value)
			}
		}
		resolutions, err := j.resolvePerformerCandidates(ctx, sc.ID, performerCandidates, sources)
		if err != nil {
			return fmt.Errorf("resolving performer candidates: %w", err)
		}
		resolvedPerformerIDs = make([]int, 0, len(resolutions))
		for _, resolution := range resolutions {
			if resolution.Status == performerResolutionExisting || resolution.Status == performerResolutionCreated {
				resolvedPerformerIDs = append(resolvedPerformerIDs, resolution.PerformerID)
			}
		}
	}

	partial := models.NewScenePartial()
	sceneDirty := false
	newPerformerIDs := resolvedPerformerIDs
	if !hasAuthoritativePerformers {
		newPerformerIDs = mergeIDs(existingPerformerIDs, resolvedPerformerIDs)
	}
	if !sameIDSet(newPerformerIDs, existingPerformerIDs) {
		partial.PerformerIDs = &models.UpdateIDs{IDs: newPerformerIDs, Mode: models.RelationshipUpdateModeSet}
		sceneDirty = true
	}
	if sc.StudioID == nil && analysis.StudioID != nil {
		partial.StudioID = models.NewOptionalInt(*analysis.StudioID)
		sceneDirty = true
	}
	if sc.StudioID == nil {
		if resolved, err := j.maybeResolveStudioCandidate(ctx, sc.ID, &analysis, sources); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			logger.Warnf("[scene metadata] scene %d: studio resolver error: %v", sc.ID, err)
		} else if resolved != nil {
			partial.StudioID = models.NewOptionalInt(resolved.StudioID)
			sceneDirty = true
			logStudioResolution(sc.ID, *resolved)
		}
	}
	if resolvedDate := analysis.Date; resolvedDate != nil {
		threshold := j.dateConfidenceThreshold()
		sameExistingDate := sc.Date != nil && sc.Date.Time.Format("2006-01-02") == resolvedDate.Date.Format("2006-01-02")
		switch {
		case sc.Date == nil && resolvedDate.Confidence >= threshold:
			partial.Date = models.NewOptionalDate(models.Date{Time: resolvedDate.Date})
			sceneDirty = true
		case sc.Date != nil && !sameExistingDate && j.input.OverwriteExistingDate && !resolvedDate.Contested && resolvedDate.Confidence >= dateOverwriteMinConfidence:
			partial.Date = models.NewOptionalDate(models.Date{Time: resolvedDate.Date})
			sceneDirty = true
		}
	}
	if analysis.Title != nil && !strings.EqualFold(strings.TrimSpace(sc.Title), strings.TrimSpace(analysis.Title.Value)) &&
		(j.input.OverwriteExistingTitle || isDefaultSceneTitle(sc.Title, primary)) {
		partial.Title = models.NewOptionalString(analysis.Title.Value)
		sceneDirty = true
	}
	if analysis.Group != nil && analysis.Group.ExistingID != nil && analysis.Group.SceneIndex != nil {
		groupID, sceneIndex := *analysis.Group.ExistingID, *analysis.Group.SceneIndex
		updated := append([]models.GroupsScenes(nil), existingGroups...)
		found, changed := false, false
		for index := range updated {
			if updated[index].GroupID != groupID {
				continue
			}
			found = true
			if updated[index].SceneIndex == nil || *updated[index].SceneIndex != sceneIndex {
				value := sceneIndex
				updated[index].SceneIndex = &value
				changed = true
			}
		}
		if !found {
			value := sceneIndex
			updated = append(updated, models.GroupsScenes{GroupID: groupID, SceneIndex: &value})
			changed = true
		}
		if changed {
			partial.GroupIDs = &models.UpdateGroupIDs{Groups: updated, Mode: models.RelationshipUpdateModeSet}
			sceneDirty = true
		}
	}

	if analysis.Diagnostics.ModelAvailable {
		logger.Infof(
			"[scene metadata] scene %d: %d unique performer candidates, %d exact matches, entity_model=available",
			sc.ID, analysis.UniquePotentialPerformers, len(analysis.MatchedPerformerIDs),
		)
	} else {
		logger.Infof(
			"[scene metadata] scene %d: %d unique performer candidates, %d exact matches, entity_model=unavailable fallback=deterministic reason=%q action=%q",
			sc.ID, analysis.UniquePotentialPerformers, len(analysis.MatchedPerformerIDs),
			analysis.Diagnostics.ModelFallbackReason,
			"Settings > Tasks > Analyze scene metadata > Download and install entity model",
		)
	}
	if !sceneDirty && !fileDirty {
		return nil
	}
	if j.input.DryRun {
		logger.Infof("[scene metadata] dry run: would update scene %d", sc.ID)
		return nil
	}
	return r.WithTxn(ctx, func(ctx context.Context) error {
		if fileDirty {
			if err := r.File.Update(ctx, primary); err != nil {
				return err
			}
		}
		if sceneDirty {
			_, err := r.Scene.UpdatePartial(ctx, sc.ID, partial)
			return err
		}
		return nil
	})
}

func isDefaultSceneTitle(title string, primary *models.VideoFile) bool {
	if strings.TrimSpace(title) == "" {
		return true
	}
	if primary == nil {
		return false
	}
	base := filepath.Base(primary.Path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	return strings.EqualFold(strings.TrimSpace(title), base) || strings.EqualFold(strings.TrimSpace(title), stem)
}

// dateOverwriteMinConfidence is the minimum confidence required to
// overwrite an already-set scene date - deliberately high, since this is a
// destructive operation. Only EXIF/container-timestamp-tier signals reach
// this without being corroborated by anything else.
const dateOverwriteMinConfidence = 0.85

func mergeIDs(existing, resolved []int) []int {
	seen := map[int]struct{}{}
	var ret []int

	for _, id := range existing {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ret = append(ret, id)
	}
	for _, id := range resolved {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ret = append(ret, id)
	}

	return ret
}

// maybeResolveStudioCandidate runs the studio resolver only when the
// deterministic analyzer did not already produce a confident studio
// ID. Returns nil with no error when there is nothing to do (no
// candidate, no provider pool, or the analyzer already resolved one).
func (j *analyzeSceneMetadataJob) maybeResolveStudioCandidate(ctx context.Context, sceneID int, analysis *metadata.Analysis, sources []metadata.Source) (*studioResolution, error) {
	if !j.studioPoolAvailable() && !j.input.UseLocalAIStudioProviderSelection {
		return nil, nil
	}
	if analysis == nil || analysis.StudioCandidate == nil {
		return nil, nil
	}
	candidate := strings.TrimSpace(analysis.StudioCandidate.Value)
	if candidate == "" {
		return nil, nil
	}
	// If the analyzer already linked the candidate to an existing
	// library studio, the deterministic path is enough.
	if analysis.StudioID != nil {
		return nil, nil
	}
	resolved, err := j.resolveStudioCandidate(ctx, sceneID, candidate, sources)
	if err != nil {
		return nil, err
	}
	if resolved.Status != studioResolutionExisting && resolved.Status != studioResolutionCreated {
		logStudioResolution(sceneID, resolved)
		return nil, nil
	}
	return &resolved, nil
}

func logStudioResolution(sceneID int, resolution studioResolution) {
	logger.Infof(
		"[scene metadata] studio_resolution scene_id=%d candidate=%q status=%q studio_id=%d matching_ids=%v provider_id=%q reason=%q",
		sceneID,
		resolution.Candidate,
		string(resolution.Status),
		resolution.StudioID,
		resolution.MatchingIDs,
		resolution.ProviderID,
		resolution.Reason,
	)
}
