package manager

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scraper"
)

type cancelingPerformerScraperCache struct {
	calls  int
	cancel context.CancelFunc
}

func (c *cancelingPerformerScraperCache) ListScrapers(_ []scraper.ScrapeContentType) []*scraper.Scraper {
	return []*scraper.Scraper{{ID: "canceling-scraper"}}
}

func (c *cancelingPerformerScraperCache) ScrapeName(_ context.Context, _, _ string, _ scraper.ScrapeContentType) ([]scraper.ScrapedContent, error) {
	c.calls++
	c.cancel()
	return nil, context.Canceled
}

func TestVerifyAndCreatePerformersStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cache := &cancelingPerformerScraperCache{cancel: cancel}
	j := &analyzeSceneMetadataJob{scraperCache: cache}

	_, err := j.verifyAndCreatePerformers(ctx, []string{"First Candidate", "Second Candidate"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verifyAndCreatePerformers() error = %v, want context.Canceled", err)
	}
	if cache.calls != 1 {
		t.Fatalf("scraper calls = %d, want 1", cache.calls)
	}
}

func TestAnalyzeSceneMetadataExecuteStopsAfterCancellation(t *testing.T) {
	r := newTestRepository(t)
	setupCtx := context.Background()
	if err := r.WithTxn(setupCtx, func(ctx context.Context) error {
		for _, title := range []string{"Alice Smith", "Betty Jones"} {
			scene := models.NewScene()
			scene.Title = title
			if err := r.Scene.Create(ctx, &scene, nil); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("creating scenes: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cache := &cancelingPerformerScraperCache{cancel: cancel}
	j := &analyzeSceneMetadataJob{
		repository:   r,
		scraperCache: cache,
	}

	if err := j.Execute(ctx, &job.Progress{}); err != nil {
		t.Fatalf("Execute() error = %v, want nil for canceled job", err)
	}
	if cache.calls != 1 {
		t.Fatalf("scraper calls = %d, want 1", cache.calls)
	}
}

func TestIdentifyPerformersChecksKnownNamesAndAliasesBeforeNewCandidates(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	performer := models.NewPerformer()
	performer.Name = "Kenzie Reeves"
	performer.Aliases = models.NewRelatedStrings([]string{"Kenz"})
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		return r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &performer})
	}); err != nil {
		t.Fatalf("creating performer: %v", err)
	}

	var knownNames *knownPerformerNameIndex
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		knownNames, err = loadKnownPerformerNames(ctx, r.Performer)
		return err
	}); err != nil {
		t.Fatalf("loading known performer names: %v", err)
	}

	j := &analyzeSceneMetadataJob{
		repository:          r,
		knownPerformerNames: knownNames,
	}
	analysis, err := j.identifyPerformers(ctx, []metadata.TextSource{{
		Text: "Kenzie Reeves - kenz - Jane Doe",
	}}, nil, nil)
	if err != nil {
		t.Fatalf("identifyPerformers() error: %v", err)
	}
	if len(analysis.MatchedIDs) != 1 || analysis.MatchedIDs[0] != performer.ID {
		t.Fatalf("matched performer IDs = %v, want [%d]", analysis.MatchedIDs, performer.ID)
	}
	if len(analysis.NewCandidates) != 1 || analysis.NewCandidates[0].Name != "Jane Doe" {
		t.Fatalf("new candidates = %v, want [Jane Doe]", analysis.NewCandidates)
	}
}

func TestIdentifyPerformersDoesNotAssignAmbiguousAlias(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()

	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		for _, name := range []string{"First Performer", "Second Performer"} {
			performer := models.NewPerformer()
			performer.Name = name
			performer.Aliases = models.NewRelatedStrings([]string{"Shared Alias"})
			if err := r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &performer}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("creating performers: %v", err)
	}

	var knownNames *knownPerformerNameIndex
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		knownNames, err = loadKnownPerformerNames(ctx, r.Performer)
		return err
	}); err != nil {
		t.Fatalf("loading known performer names: %v", err)
	}

	j := &analyzeSceneMetadataJob{
		repository:          r,
		knownPerformerNames: knownNames,
	}
	analysis, err := j.identifyPerformers(ctx, []metadata.TextSource{{
		Text: "Shared Alias",
	}}, nil, nil)
	if err != nil {
		t.Fatalf("identifyPerformers() error: %v", err)
	}
	if len(analysis.MatchedIDs) != 0 {
		t.Fatalf("matched performer IDs = %v, want none", analysis.MatchedIDs)
	}
	if len(analysis.NewCandidates) != 0 {
		t.Fatalf("new candidates = %v, want none for a known ambiguous alias", analysis.NewCandidates)
	}
}

func TestIdentifyPerformersCountsOccurrencesAndDistinctNames(t *testing.T) {
	index := &knownPerformerNameIndex{}
	index.add("Jane Doe", 42)
	j := &analyzeSceneMetadataJob{knownPerformerNames: index}
	sources := []metadata.TextSource{
		{Text: "Jane Doe", Kind: metadata.SourceNFOActor, Path: "scene.nfo", Ordinal: 0},
		{Text: "Jane Doe and Jane Doe", Kind: metadata.SourceFilename, Path: "scene.mp4", Ordinal: 0},
	}

	analysis, err := j.identifyPerformers(context.Background(), sources, nil, nil)
	if err != nil {
		t.Fatalf("identifyPerformers() error = %v", err)
	}
	if analysis.MaxSourceCount != 2 || analysis.DistinctCount != 1 {
		t.Fatalf("counts = max:%d distinct:%d, want max:2 distinct:1", analysis.MaxSourceCount, analysis.DistinctCount)
	}
	if len(analysis.SourceCounts) != 2 || analysis.SourceCounts[0].Count != 1 || analysis.SourceCounts[1].Count != 2 {
		t.Fatalf("source counts = %#v", analysis.SourceCounts)
	}
	if len(analysis.MatchedIDs) != 1 || analysis.MatchedIDs[0] != 42 {
		t.Fatalf("matched IDs = %v, want [42]", analysis.MatchedIDs)
	}
	if len(analysis.Proposals) != 3 {
		t.Fatalf("proposals = %#v, want 3 source occurrences", analysis.Proposals)
	}
}

func TestKnownPerformerNameIndexPreservesOffsets(t *testing.T) {
	index := &knownPerformerNameIndex{}
	index.add("Dœ Jane", 7)
	text := "前缀 — Dœ Jane, then Dœ Jane."
	got := index.match(text)
	if len(got) != 2 {
		t.Fatalf("match() = %#v, want 2 occurrences", got)
	}
	for _, match := range got {
		if match.PerformerID != 7 || text[match.Start:match.End] != "Dœ Jane" {
			t.Errorf("match = %#v, source slice = %q", match, text[match.Start:match.End])
		}
	}
}

func TestResolveCanonicalTitlePrecedenceAndConflict(t *testing.T) {
	nfo := metadata.TextSource{Text: "Canonical NFO Title", Kind: metadata.SourceNFOTitle, Path: "scene.nfo"}
	container := metadata.TextSource{Text: "Container Title", Kind: metadata.SourceContainerTitle, Path: "scene.mp4"}
	got := resolveCanonicalTitle([]metadata.TextSource{container, nfo}, nil)
	if got == nil || got.Title != nfo.Text || got.Contested {
		t.Fatalf("resolveCanonicalTitle() = %#v, want uncontested NFO title", got)
	}

	conflict := metadata.TextSource{Text: "Different NFO Title", Kind: metadata.SourceNFOTitle, Path: "movie.nfo"}
	got = resolveCanonicalTitle([]metadata.TextSource{nfo, conflict}, nil)
	if got == nil || !got.Contested {
		t.Fatalf("resolveCanonicalTitle() = %#v, want contested", got)
	}
}
func TestResolveSceneDateUsesFileModificationOnlyAsFallback(t *testing.T) {
	j := &analyzeSceneMetadataJob{}
	modTime := time.Date(2023, 4, 5, 6, 7, 8, 0, time.UTC)
	got := j.resolveSceneDate([]metadata.TextSource{{
		Text: "No production date here", Kind: metadata.SourceFilename, Path: "scene.mp4",
	}}, modTime)
	if got == nil || !got.Date.Equal(modTime) || got.Confidence != 0.15 {
		t.Fatalf("resolveSceneDate() = %#v, want modification-time fallback", got)
	}

	got = j.resolveSceneDate([]metadata.TextSource{{
		Text: "2020-05-01", Kind: metadata.SourceNFOPremiered, Path: "scene.nfo",
	}}, modTime)
	if got == nil || got.Date.Year() != 2020 || got.Source == "file_modification_time" {
		t.Fatalf("resolveSceneDate(production) = %#v, want production evidence", got)
	}
}

func TestResolveStudioUsesExactKnownAliasBeforeModelFallback(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()
	studio := models.NewStudio()
	studio.Name = "Example Studio"
	studio.Aliases = models.NewRelatedStrings([]string{"Example Films"})
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		return r.Studio.Create(ctx, &models.CreateStudioInput{Studio: &studio})
	}); err != nil {
		t.Fatalf("creating studio: %v", err)
	}

	var known *knownPerformerNameIndex
	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		var err error
		known, err = loadKnownStudioNames(ctx, r.Studio)
		return err
	}); err != nil {
		t.Fatalf("loading studio names: %v", err)
	}
	j := &analyzeSceneMetadataJob{repository: r, knownStudioNames: known}
	source := metadata.TextSource{
		Text: "Example Films - Jane Doe - Scene Title",
		Kind: metadata.SourceFilename,
		Path: "/library/scene.mp4",
	}
	spans := j.extractSourceSpans(ctx, source, nil)
	resolved, err := j.resolveStudio(ctx, nil, []metadata.TextSource{source}, spans)
	if err != nil {
		t.Fatalf("resolveStudio() error = %v", err)
	}
	if resolved == nil || resolved.ID != studio.ID {
		t.Fatalf("resolveStudio() = %#v, want studio %d", resolved, studio.ID)
	}
	for _, span := range spans {
		if span.Kind == metadata.EntityPerformer && canonicalGroupName(span.Text) == "example films" {
			t.Fatalf("known studio alias also emitted as performer: %#v", span)
		}
	}
}

func TestResolveMovieUsesKnownGroupAndStructuredSceneNumber(t *testing.T) {
	group := &models.Group{ID: 9, Name: "Sample Movie"}
	index := &knownGroupNameIndex{byName: make(map[string]*models.Group)}
	index.add(group.Name, group)
	j := &analyzeSceneMetadataJob{knownGroupNames: index}
	sources := []metadata.TextSource{
		{Text: "Sample Movie", Kind: metadata.SourceNFOSet, Path: "scene.nfo"},
		{Text: "2", Kind: metadata.SourceContainerEpisode, Path: "scene.mp4"},
	}
	got := j.resolveMovie(sources, nil)
	if got == nil || got.Contested || got.Group == nil || got.Group.ID != group.ID || got.SceneNumber == nil || *got.SceneNumber != 2 {
		t.Fatalf("resolveMovie() = %#v", got)
	}

	sources = append(sources, metadata.TextSource{Text: "3", Kind: metadata.SourceContainerEpisode, Path: "scene.mp4", Ordinal: 1})
	if got := j.resolveMovie(sources, nil); got == nil || !got.Contested {
		t.Fatalf("resolveMovie(conflict) = %#v, want contested", got)
	}
}

func TestAnalyzeSceneMetadataWritesCombinedResultAndPreservesDryRun(t *testing.T) {
	r := newTestRepository(t)
	ctx := context.Background()
	performer := models.NewPerformer()
	performer.Name = "Kenzie Reeves"
	var writeSceneID, dryRunSceneID int
	if err := r.WithTxn(ctx, func(ctx context.Context) error {
		if err := r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &performer}); err != nil {
			return err
		}
		for i, title := range []string{
			"Kenzie Reeves - 2020-05-01 - Canonical Scene",
			"Kenzie Reeves - 2021-06-02 - Dry Run Scene",
		} {
			scene := models.NewScene()
			scene.Title = title
			if err := r.Scene.Create(ctx, &scene, nil); err != nil {
				return err
			}
			if i == 0 {
				writeSceneID = scene.ID
			} else {
				dryRunSceneID = scene.ID
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("creating fixture: %v", err)
	}

	for _, run := range []struct {
		id     int
		dryRun bool
	}{
		{id: writeSceneID},
		{id: dryRunSceneID, dryRun: true},
	} {
		j := &analyzeSceneMetadataJob{
			repository: r,
			input: AnalyzeSceneMetadataInput{
				SceneIDs: []string{fmt.Sprint(run.id)},
				DryRun:   run.dryRun,
			},
		}
		if err := j.Execute(ctx, &job.Progress{}); err != nil {
			t.Fatalf("Execute(scene %d) error = %v", run.id, err)
		}
	}

	if err := r.WithReadTxn(ctx, func(ctx context.Context) error {
		written, err := r.Scene.Find(ctx, writeSceneID)
		if err != nil {
			return err
		}
		if err := written.LoadPerformerIDs(ctx, r.Scene); err != nil {
			return err
		}
		if got := written.PerformerIDs.List(); len(got) != 1 || got[0] != performer.ID {
			t.Errorf("written performer IDs = %v, want [%d]", got, performer.ID)
		}
		wantDate := models.Date{Time: time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)}
		if written.Date == nil || !written.Date.Equal(wantDate.Time) {
			t.Errorf("written date = %v, want %v", written.Date, wantDate)
		}

		dryRun, err := r.Scene.Find(ctx, dryRunSceneID)
		if err != nil {
			return err
		}
		if err := dryRun.LoadPerformerIDs(ctx, r.Scene); err != nil {
			return err
		}
		if got := dryRun.PerformerIDs.List(); len(got) != 0 {
			t.Errorf("dry-run performer IDs = %v, want none", got)
		}
		if dryRun.Date != nil {
			t.Errorf("dry-run date = %v, want nil", dryRun.Date)
		}
		return nil
	}); err != nil {
		t.Fatalf("reading results: %v", err)
	}
}
