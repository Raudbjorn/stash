package manager

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/file"
	fileimage "github.com/stashapp/stash/pkg/file/image"
	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/match"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scene/metadata/gliner"
	"github.com/stashapp/stash/pkg/scraper"
	"github.com/stashapp/stash/pkg/sliceutil/stringslice"
	studioutil "github.com/stashapp/stash/pkg/studio"
)

const (
	defaultPerformerConfidenceThreshold = 0.6
	defaultDateConfidenceThreshold      = 0.6
	defaultTitleConfidenceThreshold     = 0.90

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
	UseDetails               bool     `json:"useDetails"`
	ApplyStudio              bool     `json:"applyStudio"`
	ApplyTitle               bool     `json:"applyTitle"`
	TitleConfidenceThreshold *float64 `json:"titleConfidenceThreshold"`
	OverwriteExistingTitle   bool     `json:"overwriteExistingTitle"`
	ApplyMovieGroup          bool     `json:"applyMovieGroup"`
	UseEmbeddedMetadata      bool     `json:"useEmbeddedMetadata"`
}

func (s *Manager) AnalyzeSceneMetadata(ctx context.Context, input AnalyzeSceneMetadataInput) int {
	j := &analyzeSceneMetadataJob{
		repository: s.Repository,
		input:      input,
		fs:         &file.OsFS{},
	}
	if s.FFProbe != nil {
		j.probe = s.FFProbe
	}

	return s.JobManager.Add(ctx, "Analyzing scene metadata...", j)
}

type performerScraperCache interface {
	ListScrapers([]scraper.ScrapeContentType) []*scraper.Scraper
	ScrapeName(context.Context, string, string, scraper.ScrapeContentType) ([]scraper.ScrapedContent, error)
}

type sceneMetadataProbe interface {
	NewVideoFile(string) (*ffmpeg.VideoFile, error)
}

type analyzeSceneMetadataJob struct {
	repository          models.Repository
	input               AnalyzeSceneMetadataInput
	scraperCache        performerScraperCache
	knownPerformerNames *knownPerformerNameIndex
	knownStudioNames    *knownPerformerNameIndex
	fs                  models.FS
	probe               sceneMetadataProbe
	studioMatchCache    *match.Cache
	knownGroupNames     *knownGroupNameIndex
}

type knownPerformerNameNode struct {
	children    map[string]*knownPerformerNameNode
	performerID int
	terminal    bool
}

type knownPerformerNameIndex struct {
	root knownPerformerNameNode
}

type performerNameToken struct {
	normalized string
	start      int
	end        int
}

type performerNameMatch struct {
	PerformerID int
	Start       int
	End         int
	Text        string
	Ambiguous   bool
}

func performerNameTokens(name string) []performerNameToken {
	var ret []performerNameToken
	start := -1
	for offset, r := range name {
		if unicode.IsLetter(r) {
			if start < 0 {
				start = offset
			}
			continue
		}
		if start >= 0 {
			ret = append(ret, performerNameToken{
				normalized: strings.ToLower(name[start:offset]),
				start:      start,
				end:        offset,
			})
			start = -1
		}
	}
	if start >= 0 {
		ret = append(ret, performerNameToken{
			normalized: strings.ToLower(name[start:]),
			start:      start,
			end:        len(name),
		})
	}
	return ret
}

func performerNameWords(name string) []string {
	tokens := performerNameTokens(name)
	ret := make([]string, len(tokens))
	for i := range tokens {
		ret[i] = tokens[i].normalized
	}
	return ret
}

func (i *knownPerformerNameIndex) add(name string, performerID int) {
	words := performerNameWords(name)
	if len(words) == 0 {
		return
	}

	node := &i.root
	for _, word := range words {
		if node.children == nil {
			node.children = make(map[string]*knownPerformerNameNode)
		}
		child := node.children[word]
		if child == nil {
			child = &knownPerformerNameNode{}
			node.children[word] = child
		}
		node = child
	}

	if node.terminal && node.performerID != performerID {
		node.performerID = 0
		return
	}
	node.performerID = performerID
	node.terminal = true
}

func (i *knownPerformerNameIndex) exact(name string) (int, bool) {
	node := &i.root
	for _, word := range performerNameWords(name) {
		node = node.children[word]
		if node == nil {
			return 0, false
		}
	}
	return node.performerID, node.terminal
}

func (i *knownPerformerNameIndex) match(text string) []performerNameMatch {
	tokens := performerNameTokens(text)
	var ret []performerNameMatch
	for start := range tokens {
		node := &i.root
		for end := start; end < len(tokens); end++ {
			node = node.children[tokens[end].normalized]
			if node == nil {
				break
			}
			if node.terminal {
				ret = append(ret, performerNameMatch{
					PerformerID: node.performerID,
					Start:       tokens[start].start,
					End:         tokens[end].end,
					Text:        text[tokens[start].start:tokens[end].end],
					Ambiguous:   node.performerID == 0,
				})
			}
		}
	}
	return ret
}

// loadKnownPerformerNames builds a case-insensitive, token-exact index for
// canonical performer names and aliases. Shared names remain recognizable but
// ambiguous, so they are never assigned to an arbitrary performer.
func loadKnownPerformerNames(ctx context.Context, reader models.PerformerAutoTagQueryer) (*knownPerformerNameIndex, error) {
	ignoreAutoTag := false
	perPage := -1
	performers, _, err := reader.Query(ctx, &models.PerformerFilterType{
		IgnoreAutoTag: &ignoreAutoTag,
	}, &models.FindFilterType{PerPage: &perPage})
	if err != nil {
		return nil, err
	}

	aliasesByID := make(map[int][]string, len(performers))
	if bulk, ok := reader.(models.AllAliasLoader); ok {
		aliasesByID, err = bulk.GetAllAliases(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		for _, performer := range performers {
			aliases, aliasErr := reader.GetAliases(ctx, performer.ID)
			if aliasErr != nil {
				return nil, aliasErr
			}
			aliasesByID[performer.ID] = aliases
		}
	}

	ret := &knownPerformerNameIndex{}
	for _, performer := range performers {
		ret.add(performer.Name, performer.ID)
		for _, alias := range aliasesByID[performer.ID] {
			ret.add(alias, performer.ID)
		}
	}

	return ret, nil
}

func loadKnownStudioNames(ctx context.Context, reader models.StudioReader) (*knownPerformerNameIndex, error) {
	studios, err := reader.All(ctx)
	if err != nil {
		return nil, err
	}
	aliasesByID := make(map[int][]string, len(studios))
	if bulk, ok := reader.(models.AllAliasLoader); ok {
		aliasesByID, err = bulk.GetAllAliases(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		for _, studio := range studios {
			aliasesByID[studio.ID], err = reader.GetAliases(ctx, studio.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	ret := &knownPerformerNameIndex{}
	for _, studio := range studios {
		ret.add(studio.Name, studio.ID)
		for _, alias := range aliasesByID[studio.ID] {
			ret.add(alias, studio.ID)
		}
	}
	return ret, nil
}

type knownGroupNameIndex struct {
	byName map[string]*models.Group
}

func canonicalGroupName(value string) string {
	value = strings.NewReplacer(".", " ", "_", " ").Replace(value)
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func (i *knownGroupNameIndex) add(name string, group *models.Group) {
	key := canonicalGroupName(name)
	if key == "" {
		return
	}
	if existing, found := i.byName[key]; found && existing != nil && existing.ID != group.ID {
		i.byName[key] = nil
		return
	}
	if _, found := i.byName[key]; !found {
		i.byName[key] = group
	}
}

func (i *knownGroupNameIndex) exact(name string) (*models.Group, bool) {
	group, found := i.byName[canonicalGroupName(name)]
	return group, found && group != nil
}

func loadKnownGroupNames(ctx context.Context, reader models.GroupReader) (*knownGroupNameIndex, error) {
	groups, err := reader.All(ctx)
	if err != nil {
		return nil, err
	}
	aliasesByID := make(map[int][]string, len(groups))
	if bulk, ok := reader.(models.AllAliasLoader); ok {
		aliasesByID, err = bulk.GetAllAliases(ctx)
		if err != nil {
			return nil, err
		}
	} else {
		for _, group := range groups {
			aliasesByID[group.ID], err = reader.GetAliases(ctx, group.ID)
			if err != nil {
				return nil, err
			}
		}
	}
	ret := &knownGroupNameIndex{byName: make(map[string]*models.Group)}
	for _, group := range groups {
		ret.add(group.Name, group)
		for _, alias := range aliasesByID[group.ID] {
			ret.add(alias, group)
		}
	}
	return ret, nil
}

func (j *analyzeSceneMetadataJob) Execute(ctx context.Context, progress *job.Progress) error {
	r := j.repository
	if j.scraperCache == nil && instance != nil {
		j.scraperCache = instance.ScraperCache
	}

	sceneIDs, err := stringslice.StringSliceToIntSlice(j.input.SceneIDs)
	if err != nil {
		return fmt.Errorf("parsing scene ids: %w", err)
	}

	var scenes []*models.Scene
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		if j.knownPerformerNames == nil {
			j.knownPerformerNames, err = loadKnownPerformerNames(ctx, r.Performer)
			if err != nil {
				return fmt.Errorf("loading known performer names: %w", err)
			}
		}
		if j.studioMatchCache == nil {
			j.studioMatchCache = &match.Cache{}
			if err := j.studioMatchCache.PreloadStudios(ctx, r.Studio); err != nil {
				return fmt.Errorf("preloading studios: %w", err)
			}
		}
		if j.knownStudioNames == nil {
			j.knownStudioNames, err = loadKnownStudioNames(ctx, r.Studio)
			if err != nil {
				return fmt.Errorf("loading known studio names: %w", err)
			}
		}
		if j.knownGroupNames == nil {
			j.knownGroupNames, err = loadKnownGroupNames(ctx, r.Group)
			if err != nil {
				return fmt.Errorf("loading known group names: %w", err)
			}
		}

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

	progress.SetTotal(len(scenes))

	for _, sc := range scenes {
		if job.IsCancelled(ctx) {
			return nil
		}

		var processErr error
		var analysis sceneAnalysis
		progress.ExecuteTask(fmt.Sprintf("Analyzing metadata for %s", sc.GetTitle()), func() {
			analysis, processErr = j.processScene(ctx, sc)
		})
		if processErr != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Errorf("[scene metadata] error processing scene %d: %v", sc.ID, processErr)
		}
		if processErr == nil {
			logSceneAnalysis(sc.ID, analysis)
		}

		progress.Increment()
	}

	return nil
}

func logSceneAnalysis(sceneID int, analysis sceneAnalysis) {
	performers := analysis.Performers
	logger.Infof("[scene metadata] scene %d: potential performer names max_source_count=%d distinct=%d", sceneID, performers.MaxSourceCount, performers.DistinctCount)
	for _, count := range performers.SourceCounts {
		logger.Infof("[scene metadata] scene %d: performer source kind=%s path=%q ordinal=%d count=%d", sceneID, count.Kind, count.Path, count.Ordinal, count.Count)
	}
	for _, span := range performers.Proposals {
		logger.Debugf("[scene metadata] scene %d: span kind=%s text=%q source=%s path=%q ordinal=%d confidence=%.2f extractor=%s", sceneID, span.Kind, span.Text, span.Source.Kind, span.Source.Path, span.Source.Ordinal, span.Confidence, span.Extractor)
	}
	if analysis.Title != nil {
		logger.Debugf("[scene metadata] scene %d: title proposal %q confidence=%.2f contested=%v", sceneID, analysis.Title.Title, analysis.Title.Confidence, analysis.Title.Contested)
	}
	if analysis.Studio != nil {
		logger.Debugf("[scene metadata] scene %d: studio proposal %q", sceneID, analysis.Studio.Name)
	}
	if analysis.Movie != nil {
		logger.Debugf("[scene metadata] scene %d: movie proposal group=%v scene_number=%v contested=%v", sceneID, analysis.Movie.Group, analysis.Movie.SceneNumber, analysis.Movie.Contested)
	}
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

func (j *analyzeSceneMetadataJob) titleConfidenceThreshold() float64 {
	if j.input.TitleConfidenceThreshold != nil {
		return *j.input.TitleConfidenceThreshold
	}
	return defaultTitleConfidenceThreshold
}

type sceneAnalysis struct {
	Performers performerAnalysis
	Date       *metadata.ResolvedDate
	Studio     *models.Studio
	Title      *titleResolution
	Movie      *movieResolution
}

func (j *analyzeSceneMetadataJob) processScene(ctx context.Context, sc *models.Scene) (sceneAnalysis, error) {
	r := j.repository
	var currentStudio *models.Studio
	var existingPerformerIDs []int
	var existingGroups []models.GroupsScenes
	var fileModTime time.Time
	var primary *models.VideoFile

	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		if err := sc.LoadPerformerIDs(ctx, r.Scene); err != nil {
			return err
		}
		existingPerformerIDs = sc.PerformerIDs.List()
		if err := sc.LoadGroups(ctx, r.Scene); err != nil {
			return err
		}
		existingGroups = sc.Groups.List()
		if err := sc.LoadFiles(ctx, r.Scene); err != nil {
			return err
		}
		if vf := sc.Files.Primary(); vf != nil {
			primary = vf
			fileModTime = vf.ModTime
		}
		if sc.StudioID != nil {
			var err error
			currentStudio, err = r.Studio.Find(ctx, *sc.StudioID)
			if err != nil {
				return err
			}
			if currentStudio != nil {
				if err := currentStudio.LoadAliases(ctx, r.Studio); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		return sceneAnalysis{}, fmt.Errorf("loading scene: %w", err)
	}

	evidence := j.collectEvidence(sc, primary)
	if len(evidence.Sources) == 0 {
		return sceneAnalysis{}, nil
	}
	performers, err := j.identifyPerformers(ctx, evidence.Sources, existingPerformerIDs, studioExcludeNames(currentStudio))
	if err != nil {
		return sceneAnalysis{}, fmt.Errorf("identifying performers: %w", err)
	}
	analysis := sceneAnalysis{
		Performers: performers,
		Date:       j.resolveSceneDate(evidence.Sources, fileModTime),
		Title:      resolveCanonicalTitle(evidence.Sources, performers.Proposals),
		Movie:      j.resolveMovie(evidence.Sources, performers.Proposals),
	}
	analysis.Studio, err = j.resolveStudio(ctx, primary, evidence.Sources, performers.Proposals)
	if err != nil {
		return analysis, fmt.Errorf("resolving studio: %w", err)
	}

	var createdIDs []int
	if len(performers.NewCandidates) > 0 {
		candidateNames := make([]string, len(performers.NewCandidates))
		for i := range performers.NewCandidates {
			candidateNames[i] = performers.NewCandidates[i].Name
		}
		createdIDs, err = j.verifyAndCreatePerformers(ctx, candidateNames)
		if err != nil {
			return analysis, fmt.Errorf("verifying new performer candidates: %w", err)
		}
	}

	partial := models.NewScenePartial()
	dirty := false
	newPerformerIDs := mergeIDs(existingPerformerIDs, performers.MatchedIDs, createdIDs)
	if len(newPerformerIDs) != len(existingPerformerIDs) {
		partial.PerformerIDs = &models.UpdateIDs{IDs: newPerformerIDs, Mode: models.RelationshipUpdateModeSet}
		dirty = true
	}

	if analysis.Date != nil {
		switch {
		case sc.Date == nil && analysis.Date.Confidence >= j.dateConfidenceThreshold() && !analysis.Date.Contested:
			partial.Date = models.NewOptionalDate(models.Date{Time: analysis.Date.Date})
			dirty = true
		case sc.Date != nil && j.input.OverwriteExistingDate && !analysis.Date.Contested && analysis.Date.Confidence >= dateOverwriteMinConfidence:
			partial.Date = models.NewOptionalDate(models.Date{Time: analysis.Date.Date})
			dirty = true
		}
	}

	if j.input.ApplyStudio && sc.StudioID == nil && analysis.Studio != nil {
		partial.StudioID = models.NewOptionalInt(analysis.Studio.ID)
		dirty = true
	}

	if j.input.ApplyTitle && analysis.Title != nil && !analysis.Title.Contested &&
		analysis.Title.Confidence >= j.titleConfidenceThreshold() && shouldApplyTitle(sc.Title, primary, analysis.Title.Title, j.input.OverwriteExistingTitle) {
		partial.Title = models.NewOptionalString(analysis.Title.Title)
		dirty = true
	}

	if j.input.ApplyMovieGroup && analysis.Movie != nil && !analysis.Movie.Contested && analysis.Movie.Group != nil {
		applyRelation := len(existingGroups) == 0
		conflict := false
		updateExistingIndex := false
		for _, relation := range existingGroups {
			if relation.GroupID != analysis.Movie.Group.ID {
				conflict = true
				break
			}
			if relation.SceneIndex == nil {
				applyRelation = true
				updateExistingIndex = true
			} else if analysis.Movie.SceneNumber == nil || *relation.SceneIndex != *analysis.Movie.SceneNumber {
				conflict = true
				break
			}
		}
		if conflict {
			logger.Infof("[scene metadata] scene %d: movie relationship proposal conflicts with existing groups", sc.ID)
		} else if applyRelation {
			mode := models.RelationshipUpdateModeAdd
			if updateExistingIndex {
				mode = models.RelationshipUpdateModeSet
			}
			partial.GroupIDs = &models.UpdateGroupIDs{
				Groups: []models.GroupsScenes{{GroupID: analysis.Movie.Group.ID, SceneIndex: analysis.Movie.SceneNumber}},
				Mode:   mode,
			}
			dirty = true
		}
	}

	if !dirty {
		return analysis, nil
	}
	if j.input.DryRun {
		logger.Infof("[scene metadata] dry run: would update scene %d", sc.ID)
		return analysis, nil
	}
	err = r.WithTxn(ctx, func(ctx context.Context) error {
		_, updateErr := r.Scene.UpdatePartial(ctx, sc.ID, partial)
		return updateErr
	})
	return analysis, err
}

func shouldApplyTitle(existing string, primary *models.VideoFile, proposed string, overwrite bool) bool {
	if canonicalGroupName(existing) == canonicalGroupName(proposed) {
		return false
	}
	if overwrite || strings.TrimSpace(existing) == "" {
		return true
	}
	if primary == nil {
		return false
	}
	stem := strings.TrimSuffix(filepath.Base(primary.Path), filepath.Ext(primary.Path))
	return canonicalGroupName(existing) == canonicalGroupName(stem)
}

// dateOverwriteMinConfidence is the minimum confidence required to
// overwrite an already-set scene date - deliberately high, since this is a
// destructive operation. Only EXIF/container-timestamp-tier signals reach
// this without being corroborated by anything else.
const dateOverwriteMinConfidence = 0.85

func (j *analyzeSceneMetadataJob) textSources(sc *models.Scene, filePath string) []metadata.TextSource {
	var ret []metadata.TextSource
	filename := ""
	if filePath != "" {
		filename = strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	}
	if sc.Title != "" {
		ret = append(ret, metadata.TextSource{Text: sc.Title, Kind: metadata.SourceTitle})
	}
	if filename != "" {
		ret = append(ret, metadata.TextSource{Text: filename, Kind: metadata.SourceFilename, Path: filePath})
		parent := filepath.Base(filepath.Dir(filePath))
		if parent != "" && parent != "." {
			ret = append(ret, metadata.TextSource{Text: parent, Kind: metadata.SourceParentFolder, Path: filepath.Dir(filePath)})
		}
	}
	if j.input.UseDetails && sc.Details != "" {
		ret = append(ret, metadata.TextSource{Text: sc.Details, Kind: metadata.SourceDetails})
	}
	return ret
}

type sceneMetadataEvidence struct {
	Sources []metadata.TextSource
	NFO     *metadata.NFO
	Video   *ffmpeg.VideoFile
}

func (j *analyzeSceneMetadataJob) collectEvidence(sc *models.Scene, primary *models.VideoFile) sceneMetadataEvidence {
	evidence := sceneMetadataEvidence{}
	filePath := ""
	if primary != nil {
		filePath = primary.Path
	}
	evidence.Sources = j.textSources(sc, filePath)
	if primary == nil || j.fs == nil {
		return evidence
	}

	statPath := primary.Path
	if primary.ZipFile != nil {
		statPath = primary.ZipFile.Base().Path
	}
	if _, err := j.fs.Stat(statPath); err != nil {
		logger.Warnf("[scene metadata] primary path unavailable for scene %d: %v", sc.ID, err)
		return evidence
	}

	nfo, err := metadata.ReadNFO(j.fs, primary)
	if err != nil {
		logger.Warnf("[scene metadata] NFO read failed for scene %d: %v", sc.ID, err)
	} else if nfo != nil {
		evidence.NFO = nfo
		evidence.Sources = appendNFOSources(evidence.Sources, primary.Path, nfo)
	}

	if !primary.CreationTime.IsZero() {
		evidence.Sources = append(evidence.Sources, metadata.TextSource{
			Text: primary.CreationTime.Format(time.RFC3339), Kind: metadata.SourceContainerCreationTime,
			Path: primary.Path + "#stored",
		})
	}
	if !j.input.UseEmbeddedMetadata {
		return evidence
	}

	if primary.ZipFile == nil && j.probe != nil {
		video, probeErr := j.probe.NewVideoFile(primary.Path)
		if probeErr != nil {
			logger.Warnf("[scene metadata] ffprobe failed for scene %d: %v", sc.ID, probeErr)
		} else {
			evidence.Video = video
			evidence.Sources = appendContainerSources(evidence.Sources, primary.Path, video)
		}
	}

	reader, openErr := primary.BaseFile.Open(j.fs)
	if openErr != nil {
		logger.Warnf("[scene metadata] EXIF open failed for scene %d: %v", sc.ID, openErr)
		return evidence
	}
	exif, exifErr := fileimage.ExtractExifData(reader)
	_ = reader.Close()
	if exifErr != nil && !errors.Is(exifErr, fileimage.ErrNoExifData) {
		logger.Warnf("[scene metadata] EXIF decode failed for scene %d: %v", sc.ID, exifErr)
	} else if exifErr == nil {
		evidence.Sources = appendExifSources(evidence.Sources, primary.Path, exif)
	}
	return evidence
}

func appendNFOSources(sources []metadata.TextSource, path string, nfo *metadata.NFO) []metadata.TextSource {
	add := func(kind metadata.SourceKind, text string, ordinal int) {
		if text = strings.TrimSpace(text); text != "" {
			sources = append(sources, metadata.TextSource{Text: text, Kind: kind, Path: path, Ordinal: ordinal})
		}
	}
	add(metadata.SourceNFOTitle, nfo.Title, 0)
	add(metadata.SourceNFOPremiered, nfo.Premiered, 0)
	add(metadata.SourceNFOYear, nfo.Year, 0)
	add(metadata.SourceNFOStudio, nfo.Studio, 0)
	add(metadata.SourceNFOSet, nfo.SetName, 0)
	for ordinal, actor := range nfo.Actors {
		add(metadata.SourceNFOActor, actor, ordinal)
	}
	return sources
}

func appendContainerSources(sources []metadata.TextSource, path string, video *ffmpeg.VideoFile) []metadata.TextSource {
	if video == nil {
		return sources
	}
	sources = appendTagSources(sources, video.FormatTags, path+"#format", 0)
	for index, tags := range video.StreamTags {
		sources = appendTagSources(sources, tags, fmt.Sprintf("%s#stream:%d", path, index), index)
	}
	return sources
}

func appendTagSources(sources []metadata.TextSource, tags ffmpeg.FFProbeTags, path string, ordinal int) []metadata.TextSource {
	fields := []struct {
		keys []string
		kind metadata.SourceKind
	}{
		{[]string{"title"}, metadata.SourceContainerTitle},
		{[]string{"comment"}, metadata.SourceContainerComment},
		{[]string{"description"}, metadata.SourceContainerDescription},
		{[]string{"date"}, metadata.SourceContainerDate},
		{[]string{"creation_time"}, metadata.SourceContainerCreationTime},
		{[]string{"artist", "album_artist"}, metadata.SourceContainerArtist},
		{[]string{"publisher", "copyright"}, metadata.SourceContainerPublisher},
		{[]string{"show"}, metadata.SourceContainerShow},
		{[]string{"episode_id"}, metadata.SourceContainerEpisode},
	}
	for _, field := range fields {
		for keyIndex, key := range field.keys {
			if value := strings.TrimSpace(tags.Get(key)); value != "" {
				sources = append(sources, metadata.TextSource{
					Text: value, Kind: field.kind, Path: path + ":" + key, Ordinal: ordinal + keyIndex,
				})
			}
		}
	}
	return sources
}

func appendExifSources(sources []metadata.TextSource, path string, exif map[string]interface{}) []metadata.TextSource {
	add := func(kind metadata.SourceKind, key string, ordinal int) {
		if value, ok := exif[key].(string); ok {
			if value = strings.TrimSpace(value); value != "" {
				sources = append(sources, metadata.TextSource{Text: value, Kind: kind, Path: path + "#exif:" + key, Ordinal: ordinal})
			}
		}
	}
	add(metadata.SourceExifDescription, "ImageDescription", 0)
	add(metadata.SourceExifArtist, "Artist", 0)
	add(metadata.SourceExifArtist, "Copyright", 1)
	add(metadata.SourceExifOriginalDate, "DateTimeOriginal", 0)
	add(metadata.SourceExifOriginalDate, "DateTimeDigitized", 1)
	return sources
}

type titleResolution struct {
	Title      string
	Confidence float64
	Source     metadata.TextSource
	Contested  bool
}

func resolveCanonicalTitle(sources []metadata.TextSource, spans []metadata.EntitySpan) *titleResolution {
	var nfoTitles, containerTitles []metadata.TextSource
	for _, source := range sources {
		switch source.Kind {
		case metadata.SourceNFOTitle:
			nfoTitles = append(nfoTitles, source)
		case metadata.SourceContainerTitle:
			containerTitles = append(containerTitles, source)
		}
	}
	if conflictingTextSources(nfoTitles) || conflictingTextSources(containerTitles) {
		return &titleResolution{Contested: true}
	}
	if len(nfoTitles) > 0 && validTitleCandidate(nfoTitles[0].Text) {
		return &titleResolution{Title: nfoTitles[0].Text, Confidence: 0.99, Source: nfoTitles[0]}
	}
	if len(containerTitles) > 0 && validTitleCandidate(containerTitles[0].Text) {
		return &titleResolution{Title: containerTitles[0].Text, Confidence: 0.90, Source: containerTitles[0]}
	}

	var best metadata.EntitySpan
	for _, span := range spans {
		if span.Kind == metadata.EntitySceneTitle && span.Confidence > best.Confidence && validTitleCandidate(span.Text) {
			best = span
		}
	}
	if best.Text != "" {
		return &titleResolution{Title: best.Text, Confidence: best.Confidence, Source: best.Source}
	}

	for _, source := range sources {
		if source.Kind != metadata.SourceFilename {
			continue
		}
		var sourceSpans []metadata.EntitySpan
		for _, span := range spans {
			if span.Source.Kind == source.Kind && span.Source.Path == source.Path && span.Source.Ordinal == source.Ordinal {
				sourceSpans = append(sourceSpans, span)
			}
		}
		release := metadata.ParseReleaseName(source.Text, sourceSpans)
		if release.Title != "" && validTitleCandidate(release.Title) {
			return &titleResolution{Title: release.Title, Confidence: 0.75, Source: source}
		}
	}
	return nil
}

func conflictingTextSources(sources []metadata.TextSource) bool {
	if len(sources) < 2 {
		return false
	}
	first := canonicalGroupName(sources[0].Text)
	for _, source := range sources[1:] {
		if canonicalGroupName(source.Text) != first {
			return true
		}
	}
	return false
}

var twoLetterTitlePattern = regexp.MustCompile(`\pL.*\pL`)

func validTitleCandidate(title string) bool {
	if !twoLetterTitlePattern.MatchString(title) {
		return false
	}
	return metadata.ParseReleaseName(title, nil).Title != ""
}

func (j *analyzeSceneMetadataJob) resolveStudio(ctx context.Context, primary *models.VideoFile, sources []metadata.TextSource, spans []metadata.EntitySpan) (*models.Studio, error) {
	var resolved *models.Studio
	err := j.repository.WithReadTxn(ctx, func(ctx context.Context) error {
		if primary != nil {
			var err error
			resolved, err = match.PathToStudio(ctx, primary.Path, j.repository.Studio, j.studioMatchCache, true)
			if err != nil {
				return err
			}
			if resolved != nil {
				return nil
			}
		}
		var names []string
		for _, source := range sources {
			if source.Kind == metadata.SourceNFOStudio || source.Kind == metadata.SourceContainerPublisher {
				names = append(names, source.Text)
			}
		}
		for _, span := range spans {
			if span.Kind == metadata.EntityProductionStudio {
				names = append(names, span.Text)
			}
		}
		for _, name := range names {
			studio, err := studioutil.ByName(ctx, j.repository.Studio, name)
			if err != nil {
				return err
			}
			if studio == nil {
				studio, err = studioutil.ByAlias(ctx, j.repository.Studio, name)
				if err != nil {
					return err
				}
			}
			if studio != nil {
				resolved = studio
				return nil
			}
			logger.Debugf("[scene metadata] unknown studio proposal %q", name)
		}
		return nil
	})
	return resolved, err
}

type movieResolution struct {
	Group       *models.Group
	SceneNumber *int
	Source      string
	Contested   bool
}

var exactEpisodePattern = regexp.MustCompile(`^\d{1,4}$`)

func (j *analyzeSceneMetadataJob) resolveMovie(sources []metadata.TextSource, spans []metadata.EntitySpan) *movieResolution {
	if j.knownGroupNames == nil {
		return nil
	}
	var number *int
	var names []string
	for _, source := range sources {
		switch source.Kind {
		case metadata.SourceNFOSet, metadata.SourceContainerShow:
			names = append(names, source.Text)
		case metadata.SourceContainerEpisode:
			value := strings.TrimSpace(source.Text)
			if exactEpisodePattern.MatchString(value) {
				parsed, _ := strconv.Atoi(value)
				if number != nil && *number != parsed {
					return &movieResolution{Contested: true}
				}
				number = &parsed
			}
		case metadata.SourceFilename:
			var sourceSpans []metadata.EntitySpan
			for _, span := range spans {
				if span.Source.Kind == source.Kind && span.Source.Path == source.Path && span.Source.Ordinal == source.Ordinal {
					sourceSpans = append(sourceSpans, span)
				}
			}
			release := metadata.ParseReleaseName(source.Text, sourceSpans)
			if release.SceneNumber != nil {
				if number != nil && *number != *release.SceneNumber {
					return &movieResolution{Contested: true}
				}
				number = release.SceneNumber
				if release.MovieTitle != "" {
					names = append(names, release.MovieTitle)
				}
			}
		}
	}
	if number == nil {
		return nil
	}
	var group *models.Group
	var sourceName string
	for _, name := range names {
		candidate, ok := j.knownGroupNames.exact(name)
		if !ok {
			continue
		}
		if group != nil && group.ID != candidate.ID {
			return &movieResolution{Contested: true}
		}
		group = candidate
		sourceName = name
	}
	if group == nil {
		return nil
	}
	return &movieResolution{Group: group, SceneNumber: number, Source: sourceName}
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

type performerCandidate struct {
	Name string
	Span metadata.EntitySpan
}

type performerAnalysis struct {
	MatchedIDs     []int
	NewCandidates  []performerCandidate
	SourceCounts   []metadata.SourceCount
	MaxSourceCount int
	DistinctCount  int
	Proposals      []metadata.EntitySpan
}

func (j *analyzeSceneMetadataJob) identifyPerformers(ctx context.Context, sources []metadata.TextSource, existingIDs []int, studioNames map[string]struct{}) (performerAnalysis, error) {
	var result performerAnalysis
	existing := make(map[int]struct{}, len(existingIDs))
	for _, id := range existingIDs {
		existing[id] = struct{}{}
	}
	matched := map[int]struct{}{}
	candidates := map[string]performerCandidate{}
	distinct := map[string]struct{}{}

	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return performerAnalysis{}, err
		}
		spans := j.extractSourceSpans(ctx, source, studioNames)
		spans = mergeEntitySpans(spans)
		result.Proposals = append(result.Proposals, spans...)

		count := 0
		for _, span := range spans {
			if span.Kind != metadata.EntityPerformer {
				continue
			}
			count++
			performerID := 0
			known := false
			if j.knownPerformerNames != nil {
				performerID, known = j.knownPerformerNames.exact(span.Text)
			}
			if performerID != 0 {
				distinct[fmt.Sprintf("id:%d", performerID)] = struct{}{}
				if _, present := existing[performerID]; !present {
					matched[performerID] = struct{}{}
				}
				continue
			}
			normalized := strings.Join(performerNameWords(span.Text), " ")
			if normalized == "" {
				continue
			}
			distinct["name:"+normalized] = struct{}{}
			if known || span.Extractor == metadata.ExtractorExact {
				continue
			}
			if _, found := candidates[normalized]; !found {
				candidates[normalized] = performerCandidate{Name: strings.TrimSpace(span.Text), Span: span}
			}
		}
		result.SourceCounts = append(result.SourceCounts, metadata.SourceCount{
			Kind: source.Kind, Path: source.Path, Ordinal: source.Ordinal, Count: count,
		})
		if count > result.MaxSourceCount {
			result.MaxSourceCount = count
		}
	}

	for id := range matched {
		result.MatchedIDs = append(result.MatchedIDs, id)
	}
	sort.Ints(result.MatchedIDs)
	for _, candidate := range candidates {
		result.NewCandidates = append(result.NewCandidates, candidate)
	}
	sort.Slice(result.NewCandidates, func(i, k int) bool {
		return result.NewCandidates[i].Name < result.NewCandidates[k].Name
	})
	result.DistinctCount = len(distinct)
	return result, nil
}

func (j *analyzeSceneMetadataJob) extractSourceSpans(ctx context.Context, source metadata.TextSource, studioNames map[string]struct{}) []metadata.EntitySpan {
	var spans []metadata.EntitySpan
	var structuredKind metadata.EntityKind
	switch source.Kind {
	case metadata.SourceNFOActor, metadata.SourceContainerArtist:
		structuredKind = metadata.EntityPerformer
	case metadata.SourceNFOStudio, metadata.SourceContainerPublisher:
		structuredKind = metadata.EntityProductionStudio
	case metadata.SourceNFOPremiered, metadata.SourceNFOYear, metadata.SourceContainerDate, metadata.SourceContainerCreationTime, metadata.SourceExifOriginalDate:
		structuredKind = metadata.EntityProductionDate
	case metadata.SourceNFOTitle, metadata.SourceContainerTitle:
		structuredKind = metadata.EntitySceneTitle
	case metadata.SourceNFOSet, metadata.SourceContainerShow:
		structuredKind = metadata.EntityMovieTitle
	case metadata.SourceContainerEpisode:
		structuredKind = metadata.EntitySceneNumber
	}
	if structuredKind != "" {
		spans = append(spans, metadata.EntitySpan{
			Text: source.Text, Kind: structuredKind, Start: 0, End: len(source.Text),
			Confidence: 1, Extractor: metadata.ExtractorStructured, Source: source,
		})
	}
	if j.knownPerformerNames != nil {
		for _, match := range j.knownPerformerNames.match(source.Text) {
			spans = append(spans, metadata.EntitySpan{
				Text: match.Text, Kind: metadata.EntityPerformer, Start: match.Start, End: match.End,
				Confidence: 1, Extractor: metadata.ExtractorExact, Source: source,
			})
		}
	}
	if j.knownStudioNames != nil {
		for _, match := range j.knownStudioNames.match(source.Text) {
			if match.PerformerID == 0 {
				continue
			}
			spans = append(spans, metadata.EntitySpan{
				Text: match.Text, Kind: metadata.EntityProductionStudio, Start: match.Start, End: match.End,
				Confidence: 1, Extractor: metadata.ExtractorExact, Source: source,
			})
		}
	}

	if extracted, err := sceneMetadataModels.Extract(ctx, source.Text, gliner.ProductionLabels, 0.5); err == nil {
		for _, span := range extracted {
			kind, ok := glinerEntityKind(span.Label)
			if !ok || (kind == metadata.EntityPerformer && float64(span.Score) < j.performerConfidenceThreshold()) {
				continue
			}
			spans = append(spans, metadata.EntitySpan{
				Text: span.Text, Kind: kind, Start: span.Start, End: span.End,
				Confidence: float64(span.Score), Extractor: metadata.ExtractorGLiNER, Source: source,
			})
		}
	} else if !errors.Is(err, errSceneMetadataEntityExtractorUnavailable) && !errors.Is(err, gliner.ErrClosed) && ctx.Err() == nil {
		logger.Warnf("[scene metadata] GLiNER inference failed for %s %q: %v", source.Kind, source.Path, err)
	}

	for _, candidate := range metadata.ExtractNameCandidates(source.Text, func(candidate string) bool {
		if _, excluded := studioNames[strings.ToLower(candidate)]; excluded {
			return true
		}
		if j.knownStudioNames != nil {
			_, known := j.knownStudioNames.exact(candidate)
			return known
		}
		return false
	}) {
		score := getNamePlausibilityScorer().Score(candidate)
		if score < minCandidatePlausibility || score < j.performerConfidenceThreshold() {
			continue
		}
		for _, location := range candidateLocations(source.Text, candidate) {
			spans = append(spans, metadata.EntitySpan{
				Text: source.Text[location[0]:location[1]], Kind: metadata.EntityPerformer,
				Start: location[0], End: location[1], Confidence: score,
				Extractor: metadata.ExtractorLegacy, Source: source,
			})
		}
	}
	return spans
}

func candidateLocations(text, candidate string) [][2]int {
	textTokens := performerNameTokens(text)
	candidateTokens := performerNameTokens(candidate)
	if len(candidateTokens) == 0 || len(candidateTokens) > len(textTokens) {
		return nil
	}
	var ret [][2]int
	for start := range len(textTokens) - len(candidateTokens) + 1 {
		matches := true
		for offset := range candidateTokens {
			if textTokens[start+offset].normalized != candidateTokens[offset].normalized {
				matches = false
				break
			}
		}
		if matches {
			ret = append(ret, [2]int{textTokens[start].start, textTokens[start+len(candidateTokens)-1].end})
		}
	}
	return ret
}

func glinerEntityKind(label string) (metadata.EntityKind, bool) {
	switch label {
	case "person":
		return metadata.EntityPerformer, true
	case "production studio":
		return metadata.EntityProductionStudio, true
	case "production date":
		return metadata.EntityProductionDate, true
	case "scene title":
		return metadata.EntitySceneTitle, true
	case "movie title":
		return metadata.EntityMovieTitle, true
	case "scene number":
		return metadata.EntitySceneNumber, true
	case "release group":
		return metadata.EntityReleaseGroup, true
	default:
		return "", false
	}
}

func mergeEntitySpans(spans []metadata.EntitySpan) []metadata.EntitySpan {
	priority := func(extractor metadata.ExtractorKind) int {
		switch extractor {
		case metadata.ExtractorStructured:
			return 4
		case metadata.ExtractorExact:
			return 3
		case metadata.ExtractorGLiNER:
			return 2
		default:
			return 1
		}
	}
	sort.SliceStable(spans, func(i, k int) bool {
		if priority(spans[i].Extractor) != priority(spans[k].Extractor) {
			return priority(spans[i].Extractor) > priority(spans[k].Extractor)
		}
		return spans[i].Confidence > spans[k].Confidence
	})
	var ret []metadata.EntitySpan
	for _, span := range spans {
		duplicate := false
		for _, existing := range ret {
			if span.Kind == existing.Kind && span.Start < existing.End && existing.Start < span.End {
				duplicate = true
				break
			}
		}
		if !duplicate {
			ret = append(ret, span)
		}
	}
	sort.SliceStable(ret, func(i, k int) bool {
		if ret[i].Source.Ordinal != ret[k].Source.Ordinal {
			return ret[i].Source.Ordinal < ret[k].Source.Ordinal
		}
		return ret[i].Start < ret[k].Start
	})
	return ret
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
		if err := ctx.Err(); err != nil {
			// Creations commit individually; a later run matches and links any
			// performers left unlinked by cancellation.
			return nil, err
		}
		if j.input.DryRun {
			logger.Infof("[scene metadata] dry run: would look up new performer candidate %q", candidate)
			continue
		}

		verified, err := j.scrapeVerifyPerformer(ctx, scrapers, candidate)
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

func (j *analyzeSceneMetadataJob) scrapeVerifyPerformer(ctx context.Context, scrapers []*scraper.Scraper, candidate string) (string, error) {
	for _, s := range scrapers {
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

func (j *analyzeSceneMetadataJob) resolveSceneDate(sources []metadata.TextSource, fileModTime time.Time) *metadata.ResolvedDate {
	var signals []metadata.DateSignal
	for _, source := range sources {
		sourceName := fmt.Sprintf("%s:%s:%d", source.Kind, source.Path, source.Ordinal)
		text := source.Text
		if source.Kind == metadata.SourceExifOriginalDate {
			text = strings.ReplaceAll(text, ":", "-")
		}
		extracted := metadata.ExtractDateSignals(text, sourceName)
		for i := range extracted {
			switch source.Kind {
			case metadata.SourceNFOPremiered, metadata.SourceExifOriginalDate:
				extracted[i].Priority = metadata.DatePriorityProduction
			case metadata.SourceNFOYear:
				extracted[i].Priority = metadata.DatePriorityTextYearOnly
			case metadata.SourceContainerDate:
				extracted[i].Priority = metadata.DatePriorityContainerDate
			case metadata.SourceContainerCreationTime:
				extracted[i].Priority = metadata.DatePriorityContainerCreation
			}
		}
		if len(extracted) == 0 {
			switch source.Kind {
			case metadata.SourceNFOPremiered, metadata.SourceNFOYear, metadata.SourceContainerDate, metadata.SourceContainerCreationTime, metadata.SourceExifOriginalDate:
				logger.Debugf("[scene metadata] ignored malformed structured date %q from %s", source.Text, sourceName)
			}
		}
		signals = append(signals, extracted...)
	}
	if len(signals) == 0 && !fileModTime.IsZero() {
		signals = append(signals, metadata.DateSignal{
			Date: fileModTime, Priority: metadata.DatePriorityFileModification, Source: "file_modification_time",
		})
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
