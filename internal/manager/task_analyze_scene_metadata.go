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
	// PerformerVerifierScraperIDs limits new-performer name verification to
	// the configured scrapers. Omitted or empty disables network verification.
	PerformerVerifierScraperIDs []string `json:"performerVerifierScraperIDs"`
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
	// OverwriteExistingTitle allows replacing a user-authored scene title.
	// Without it, only empty or filename-derived default titles are changed.
	OverwriteExistingTitle bool `json:"overwriteExistingTitle"`
}

func (s *Manager) AnalyzeSceneMetadata(ctx context.Context, input AnalyzeSceneMetadataInput) int {
	j := &analyzeSceneMetadataJob{
		repository: s.Repository,
		input:      input,
		ffprobe:    s.FFProbe,
	}

	return s.JobManager.Add(ctx, "Analyzing scene metadata...", j)
}

type performerScraperCache interface {
	ListScrapers([]scraper.ScrapeContentType) []*scraper.Scraper
	ScrapeName(context.Context, string, string, scraper.ScrapeContentType) ([]scraper.ScrapedContent, error)
}

type analyzeSceneMetadataJob struct {
	repository                models.Repository
	input                     AnalyzeSceneMetadataInput
	scraperCache              performerScraperCache
	ffprobe                   *ffmpeg.FFProbe
	performerRecords          []metadata.NamedAliases
	studioRecords             []metadata.NamedAliases
	groupRecords              []metadata.NamedAliases
	performerVerifierScrapers []*scraper.Scraper
}

func (j *analyzeSceneMetadataJob) Execute(ctx context.Context, progress *job.Progress) error {
	r := j.repository
	if j.scraperCache == nil {
		j.scraperCache = instance.ScraperCache
	}
	j.resolvePerformerVerifierScrapers()

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
	if len(sources) == 0 {
		return nil
	}

	var exactSpans []metadata.Span
	for _, source := range sources {
		exactSpans = append(exactSpans, metadata.FindExactNamedSpans(source, j.performerRecords, metadata.EntityLabelPerformer)...)
		exactSpans = append(exactSpans, metadata.FindExactNamedSpans(source, j.studioRecords, metadata.EntityLabelStudio)...)
		exactSpans = append(exactSpans, metadata.FindExactNamedSpans(source, j.groupRecords, metadata.EntityLabelMovie)...)
	}
	analysis, err := (metadata.Analyzer{}).Analyze(ctx, metadata.Inputs{
		Sources: sources, EntityExtractor: getSceneMetadataEntityExtractor(),
		AdditionalSpans: exactSpans, DateSignals: dateSignals, SanityBound: sanityBound,
	})
	if err != nil {
		return fmt.Errorf("analyzing typed metadata: %w", err)
	}

	var newCandidates []string
	for _, candidate := range analysis.PerformerCandidates {
		if candidate.ExistingEntityID == nil && candidate.Confidence >= j.performerLookupThreshold() {
			newCandidates = append(newCandidates, candidate.Value)
		}
	}
	var createdIDs []int
	if !j.input.DryRun && len(newCandidates) > 0 {
		createdIDs, err = j.verifyAndCreatePerformers(ctx, newCandidates)
		if err != nil {
			return fmt.Errorf("verifying new performer candidates: %w", err)
		}
	}

	partial := models.NewScenePartial()
	sceneDirty := false
	newPerformerIDs := mergeIDs(existingPerformerIDs, analysis.MatchedPerformerIDs, createdIDs)
	if len(newPerformerIDs) != len(existingPerformerIDs) {
		partial.PerformerIDs = &models.UpdateIDs{IDs: newPerformerIDs, Mode: models.RelationshipUpdateModeSet}
		sceneDirty = true
	}
	if sc.StudioID == nil && analysis.StudioID != nil {
		partial.StudioID = models.NewOptionalInt(*analysis.StudioID)
		sceneDirty = true
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

	logger.Infof(
		"[scene metadata] scene %d: %d unique performer candidates, %d exact matches, model available %v",
		sc.ID, analysis.UniquePotentialPerformers, len(analysis.MatchedPerformerIDs), analysis.Diagnostics.ModelAvailable,
	)
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

// verifyAndCreatePerformers looks up each candidate via configured
// performer scrapers; a candidate is only created as a new Performer if a
// scraper confirms a performer by that exact (case-insensitive) name.
func (j *analyzeSceneMetadataJob) verifyAndCreatePerformers(ctx context.Context, candidates []string) ([]int, error) {
	if j.scraperCache == nil || len(j.performerVerifierScrapers) == 0 {
		return nil, nil
	}

	var createdIDs []int

	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			// Creations commit individually; a later run matches and links any
			// performers left unlinked by cancellation.
			return nil, err
		}
		if j.input.DryRun {
			logger.Infof("[scene metadata] dry run: would look up new performer candidate %q", candidate)
			continue
		}

		verified, err := j.scrapeVerifyPerformer(ctx, candidate)
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			logger.Warnf("[scene metadata] error verifying performer candidate %q: %v", candidate, err)
			continue
		}
		if verified == "" {
			continue
		}

		id, err := j.createPerformer(ctx, verified)
		if ctx.Err() != nil {
			// createPerformer may have committed just before cancellation. A
			// later run will match and link that performer to the scene.
			return nil, ctx.Err()
		}
		if err != nil {
			logger.Warnf("[scene metadata] error creating performer %q: %v", verified, err)
			continue
		}

		createdIDs = append(createdIDs, id)
	}

	return createdIDs, nil
}

func (j *analyzeSceneMetadataJob) scrapeVerifyPerformer(ctx context.Context, candidate string) (string, error) {
	for _, s := range j.performerVerifierScrapers {
		if err := ctx.Err(); err != nil {
			return "", err
		}
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

// createPerformer creates a new performer with the given (scraper-verified,
// exact) name.
//
// j.performerRecords - and the newCandidates list derived from it - is a
// snapshot taken once at job start (see loadLibraryRecords) and is never
// refreshed mid-run. A name that was genuinely new when that snapshot was
// taken, and again when the scraper verified it, may since have been
// created by an earlier scene processed in this same job run (or, in
// principle, by any other writer in this process). Re-checking the live
// database immediately before writing, inside the same write transaction
// as the Create, closes that window: this app's sqlite write pool has
// exactly one connection (pkg/sqlite maxWriteConnections), so every
// writable transaction in this process is fully serialized and nothing can
// create a same-named performer between our check and our write.
//
// The name match is intentionally exact and case-sensitive (nocase=false):
// the performers_name_unique index this collides with has no COLLATE
// NOCASE, so it's case-sensitive too. Matching case-insensitively here
// would risk silently merging two legitimately distinct, differently-cased
// performer records - a behavior change well beyond fixing this race.
func (j *analyzeSceneMetadataJob) createPerformer(ctx context.Context, name string) (int, error) {
	r := j.repository

	var id int
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		existing, err := r.Performer.FindByNames(ctx, []string{name}, false)
		if err != nil {
			return fmt.Errorf("checking for existing performer %q: %w", name, err)
		}
		if len(existing) > 0 {
			id = existing[0].ID
			logger.Infof("[scene metadata] performer %q already exists (id %d); skipping creation", name, id)
			return nil
		}

		newPerformer := models.NewPerformer()
		newPerformer.Name = name

		createErr := r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &newPerformer})
		if createErr == nil {
			id = newPerformer.ID
			logger.Infof("[scene metadata] created new performer %q (id %d)", name, id)
			return nil
		}

		// The check above should make this unreachable in normal operation,
		// but recover gracefully rather than dropping the performer.
		existing, findErr := r.Performer.FindByNames(ctx, []string{name}, false)
		if findErr != nil || len(existing) == 0 {
			return fmt.Errorf("creating performer %q: %w", name, createErr)
		}

		id = existing[0].ID
		logger.Infof("[scene metadata] performer %q was created concurrently (id %d); using existing record", name, id)
		return nil
	}); err != nil {
		return 0, err
	}

	return id, nil
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
