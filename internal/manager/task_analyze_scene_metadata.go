package manager

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/match"
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
	UseDetails bool `json:"useDetails"`
}

func (s *Manager) AnalyzeSceneMetadata(ctx context.Context, input AnalyzeSceneMetadataInput) int {
	j := &analyzeSceneMetadataJob{
		repository: s.Repository,
		input:      input,
	}

	return s.JobManager.Add(ctx, "Analyzing scene metadata...", j)
}

type analyzeSceneMetadataJob struct {
	repository   models.Repository
	input        AnalyzeSceneMetadataInput
	scraperCache *scraper.Cache
}

func (j *analyzeSceneMetadataJob) Execute(ctx context.Context, progress *job.Progress) error {
	r := j.repository
	j.scraperCache = instance.ScraperCache

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
		return fmt.Errorf("finding scenes: %w", err)
	}

	progress.SetTotal(len(scenes))

	for _, sc := range scenes {
		if job.IsCancelled(ctx) {
			return nil
		}

		progress.ExecuteTask(fmt.Sprintf("Analyzing metadata for %s", sc.GetTitle()), func() {
			if err := j.processScene(ctx, sc); err != nil {
				logger.Errorf("[scene metadata] error processing scene %d: %v", sc.ID, err)
			}
		})

		progress.Increment()
	}

	return nil
}

func (j *analyzeSceneMetadataJob) performerConfidenceThreshold() float64 {
	if j.input.PerformerConfidenceThreshold != nil {
		return *j.input.PerformerConfidenceThreshold
	}
	return defaultPerformerConfidenceThreshold
}

func (j *analyzeSceneMetadataJob) dateConfidenceThreshold() float64 {
	if j.input.DateConfidenceThreshold != nil {
		return *j.input.DateConfidenceThreshold
	}
	return defaultDateConfidenceThreshold
}

func (j *analyzeSceneMetadataJob) processScene(ctx context.Context, sc *models.Scene) error {
	r := j.repository

	var studio *models.Studio
	var existingPerformerIDs []int
	var filePath string
	var fileModTime time.Time
	var videoCreationTime time.Time

	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		if err := sc.LoadPerformerIDs(ctx, r.Scene); err != nil {
			return err
		}
		existingPerformerIDs = sc.PerformerIDs.List()

		if err := sc.LoadFiles(ctx, r.Scene); err != nil {
			return err
		}
		if vf := sc.Files.Primary(); vf != nil {
			filePath = vf.Path
			fileModTime = vf.ModTime
			videoCreationTime = vf.CreationTime
		}

		if sc.StudioID != nil {
			var err error
			studio, err = r.Studio.Find(ctx, *sc.StudioID)
			if err != nil {
				return err
			}
			if studio != nil {
				if err := studio.LoadAliases(ctx, r.Studio); err != nil {
					return err
				}
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("loading scene: %w", err)
	}

	textSources := j.textSources(sc, filePath)
	if len(textSources) == 0 {
		return nil
	}

	studioNames := studioExcludeNames(studio)

	matchedIDs, newCandidates, err := j.identifyPerformers(ctx, textSources, existingPerformerIDs, studioNames)
	if err != nil {
		return fmt.Errorf("identifying performers: %w", err)
	}

	var createdIDs []int
	if len(newCandidates) > 0 {
		createdIDs, err = j.verifyAndCreatePerformers(ctx, newCandidates)
		if err != nil {
			return fmt.Errorf("verifying new performer candidates: %w", err)
		}
	}

	resolvedDate := j.resolveSceneDate(sc, textSources, videoCreationTime, fileModTime)

	partial := models.NewScenePartial()
	dirty := false

	newPerformerIDs := mergeIDs(existingPerformerIDs, matchedIDs, createdIDs)
	if len(newPerformerIDs) != len(existingPerformerIDs) {
		partial.PerformerIDs = &models.UpdateIDs{
			IDs:  newPerformerIDs,
			Mode: models.RelationshipUpdateModeSet,
		}
		dirty = true
	}

	if resolvedDate != nil {
		threshold := j.dateConfidenceThreshold()
		switch {
		case sc.Date == nil && resolvedDate.Confidence >= threshold:
			partial.Date = models.NewOptionalDate(models.Date{Time: resolvedDate.Date})
			dirty = true
			logger.Infof("[scene metadata] scene %d: setting date %s (confidence %.2f, source %s)", sc.ID, resolvedDate.Date.Format("2006-01-02"), resolvedDate.Confidence, resolvedDate.Source)
		case sc.Date != nil && j.input.OverwriteExistingDate && !resolvedDate.Contested && resolvedDate.Confidence >= dateOverwriteMinConfidence:
			partial.Date = models.NewOptionalDate(models.Date{Time: resolvedDate.Date})
			dirty = true
			logger.Infof("[scene metadata] scene %d: overwriting date with %s (confidence %.2f, source %s)", sc.ID, resolvedDate.Date.Format("2006-01-02"), resolvedDate.Confidence, resolvedDate.Source)
		default:
			logger.Debugf("[scene metadata] scene %d: date %s not applied (confidence %.2f, contested %v, existing date present %v)", sc.ID, resolvedDate.Date.Format("2006-01-02"), resolvedDate.Confidence, resolvedDate.Contested, sc.Date != nil)
		}
	}

	if !dirty {
		return nil
	}

	if j.input.DryRun {
		logger.Infof("[scene metadata] dry run: would update scene %d", sc.ID)
		return nil
	}

	return r.WithTxn(ctx, func(ctx context.Context) error {
		_, err := r.Scene.UpdatePartial(ctx, sc.ID, partial)
		return err
	})
}

// dateOverwriteMinConfidence is the minimum confidence required to
// overwrite an already-set scene date - deliberately high, since this is a
// destructive operation. Only EXIF/container-timestamp-tier signals reach
// this without being corroborated by anything else.
const dateOverwriteMinConfidence = 0.85

type sceneTextSource struct {
	text   string
	source string
}

func (j *analyzeSceneMetadataJob) textSources(sc *models.Scene, filePath string) []sceneTextSource {
	var ret []sceneTextSource

	filename := ""
	if filePath != "" {
		filename = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}

	if sc.Title != "" && !strings.EqualFold(sc.Title, filename) {
		ret = append(ret, sceneTextSource{text: sc.Title, source: "title"})
	}
	if filename != "" {
		ret = append(ret, sceneTextSource{text: filename, source: "filename"})
	}
	if j.input.UseDetails && sc.Details != "" {
		ret = append(ret, sceneTextSource{text: sc.Details, source: "details"})
	}

	return ret
}

func studioExcludeNames(studio *models.Studio) map[string]struct{} {
	ret := map[string]struct{}{}
	if studio == nil {
		return ret
	}

	ret[strings.ToLower(studio.Name)] = struct{}{}
	for _, alias := range studio.Aliases.List() {
		ret[strings.ToLower(alias)] = struct{}{}
	}

	return ret
}

// identifyPerformers matches known performers against the scene's text
// sources (reusing pkg/match's existing path-matching machinery - it works
// on any string, not just filesystem paths) and separately extracts
// candidate names for performers not yet in the library. Returns matched
// performer IDs not already on the scene, and surviving new-name
// candidates (deduplicated, plausibility-filtered).
func (j *analyzeSceneMetadataJob) identifyPerformers(ctx context.Context, sources []sceneTextSource, existingIDs []int, studioNames map[string]struct{}) (matchedIDs []int, newCandidates []string, err error) {
	r := j.repository

	existing := map[int]struct{}{}
	for _, id := range existingIDs {
		existing[id] = struct{}{}
	}

	matchedSet := map[int]struct{}{}
	candidateSet := map[string]struct{}{}

	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		for _, src := range sources {
			performers, err := match.PathToPerformers(ctx, src.text, r.Performer, nil, false)
			if err != nil {
				return err
			}

			for _, p := range performers {
				if _, ok := existing[p.ID]; ok {
					continue
				}
				matchedSet[p.ID] = struct{}{}
			}
		}

		return nil
	}); err != nil {
		return nil, nil, err
	}

	for _, src := range sources {
		candidates := metadata.ExtractNameCandidates(src.text, func(c string) bool {
			_, excluded := studioNames[c]
			return excluded
		})

		for _, c := range candidates {
			key := strings.ToLower(c)
			if _, ok := candidateSet[key]; ok {
				continue
			}
			if getNamePlausibilityScorer().Score(c) < minCandidatePlausibility {
				continue
			}
			candidateSet[key] = struct{}{}
			newCandidates = append(newCandidates, c)
		}
	}

	for id := range matchedSet {
		matchedIDs = append(matchedIDs, id)
	}

	return matchedIDs, newCandidates, nil
}

// verifyAndCreatePerformers looks up each candidate via configured
// performer scrapers; a candidate is only created as a new Performer if a
// scraper confirms a performer by that exact (case-insensitive) name.
func (j *analyzeSceneMetadataJob) verifyAndCreatePerformers(ctx context.Context, candidates []string) ([]int, error) {
	if j.scraperCache == nil {
		return nil, nil
	}

	scrapers := j.scraperCache.ListScrapers([]scraper.ScrapeContentType{scraper.ScrapeContentTypePerformer})
	if len(scrapers) == 0 {
		return nil, nil
	}

	var createdIDs []int

	for _, candidate := range candidates {
		if j.input.DryRun {
			logger.Infof("[scene metadata] dry run: would look up new performer candidate %q", candidate)
			continue
		}

		verified, err := j.scrapeVerifyPerformer(ctx, scrapers, candidate)
		if err != nil {
			logger.Warnf("[scene metadata] error verifying performer candidate %q: %v", candidate, err)
			continue
		}
		if verified == "" {
			continue
		}

		id, err := j.createPerformer(ctx, verified)
		if err != nil {
			logger.Warnf("[scene metadata] error creating performer %q: %v", verified, err)
			continue
		}

		createdIDs = append(createdIDs, id)
	}

	return createdIDs, nil
}

func (j *analyzeSceneMetadataJob) scrapeVerifyPerformer(ctx context.Context, scrapers []*scraper.Scraper, candidate string) (string, error) {
	for _, s := range scrapers {
		content, err := j.scraperCache.ScrapeName(ctx, s.ID, candidate, scraper.ScrapeContentTypePerformer)
		if err != nil {
			logger.Debugf("[scene metadata] scraper %s lookup for %q failed: %v", s.ID, candidate, err)
			continue
		}

		for _, c := range content {
			var name string
			switch p := c.(type) {
			case *models.ScrapedPerformer:
				if p != nil && p.Name != nil {
					name = *p.Name
				}
			case models.ScrapedPerformer:
				if p.Name != nil {
					name = *p.Name
				}
			}

			if name != "" && strings.EqualFold(name, candidate) {
				return name, nil
			}
		}
	}

	return "", nil
}

func (j *analyzeSceneMetadataJob) createPerformer(ctx context.Context, name string) (int, error) {
	r := j.repository

	newPerformer := models.NewPerformer()
	newPerformer.Name = name

	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		return r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &newPerformer})
	}); err != nil {
		return 0, err
	}

	logger.Infof("[scene metadata] created new performer %q (id %d)", name, newPerformer.ID)

	return newPerformer.ID, nil
}

func (j *analyzeSceneMetadataJob) resolveSceneDate(sc *models.Scene, sources []sceneTextSource, videoCreationTime, fileModTime time.Time) *metadata.ResolvedDate {
	var signals []metadata.DateSignal

	if !videoCreationTime.IsZero() {
		signals = append(signals, metadata.DateSignal{
			Date:     videoCreationTime,
			Priority: metadata.DatePriorityVideoCreation,
			Source:   "video_creation_time",
		})
	}

	for _, src := range sources {
		signals = append(signals, metadata.ExtractDateSignals(src.text, src.source)...)
	}

	if len(signals) == 0 {
		return nil
	}

	sanityBound := fileModTime
	if sanityBound.IsZero() {
		sanityBound = time.Now()
	}

	return metadata.ResolveDate(signals, sanityBound)
}

func mergeIDs(existing, matched, created []int) []int {
	seen := map[int]struct{}{}
	var ret []int

	for _, id := range existing {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ret = append(ret, id)
	}
	for _, id := range matched {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ret = append(ret, id)
	}
	for _, id := range created {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ret = append(ret, id)
	}

	return ret
}
