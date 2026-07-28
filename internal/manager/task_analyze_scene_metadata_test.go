package manager

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scraper"
)

type performerScrapeCall struct {
	scraperID string
	candidate string
}

type recordingPerformerScraperCache struct {
	scrapers    []*scraper.Scraper
	listCalls   int
	calls       []performerScrapeCall
	responses   map[performerScrapeCall][]scraper.ScrapedContent
	cancel      context.CancelFunc
	cancelAfter int
}

func (c *recordingPerformerScraperCache) ListScrapers(_ []scraper.ScrapeContentType) []*scraper.Scraper {
	c.listCalls++
	return c.scrapers
}

func (c *recordingPerformerScraperCache) ScrapeName(_ context.Context, scraperID, candidate string, _ scraper.ScrapeContentType) ([]scraper.ScrapedContent, error) {
	call := performerScrapeCall{scraperID: scraperID, candidate: candidate}
	c.calls = append(c.calls, call)
	if c.cancel != nil && len(c.calls) == c.cancelAfter {
		c.cancel()
		return nil, context.Canceled
	}
	return c.responses[call], nil
}

func performerScraper(id string, scrapeTypes ...scraper.ScrapeType) *scraper.Scraper {
	return &scraper.Scraper{
		ID: id,
		Performer: &scraper.ScraperSpec{
			SupportedScrapes: scrapeTypes,
		},
	}
}

func TestResolvePerformerVerifierScrapersPreservesRequestedOrder(t *testing.T) {
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{
			performerScraper("first", scraper.ScrapeTypeName),
			performerScraper("second", scraper.ScrapeTypeName),
			performerScraper("url-only", scraper.ScrapeTypeURL),
			performerScraper("fragment-only", scraper.ScrapeTypeFragment),
			performerScraper("hybrid", scraper.ScrapeTypeURL, scraper.ScrapeTypeName),
		},
	}
	j := &analyzeSceneMetadataJob{
		input: AnalyzeSceneMetadataInput{
			PerformerVerifierScraperIDs: []string{
				"second", "missing", "url-only", "fragment-only",
				"hybrid", "first", "second", "missing",
			},
		},
		scraperCache: cache,
	}

	j.resolvePerformerVerifierScrapers()

	var got []string
	for _, s := range j.performerVerifierScrapers {
		got = append(got, s.ID)
	}
	want := []string{"second", "hybrid", "first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved scraper IDs = %#v, want %#v", got, want)
	}
	if cache.listCalls != 1 {
		t.Fatalf("ListScrapers() calls = %d, want 1", cache.listCalls)
	}
}

func TestVerifyAndCreatePerformersSkipsIneligibleSelections(t *testing.T) {
	tests := []struct {
		name       string
		selectedID string
		available  *scraper.Scraper
	}{
		{name: "empty selection"},
		{name: "unknown scraper", selectedID: "missing"},
		{
			name:       "URL-only scraper",
			selectedID: "url-only",
			available:  performerScraper("url-only", scraper.ScrapeTypeURL),
		},
		{
			name:       "fragment-only scraper",
			selectedID: "fragment-only",
			available:  performerScraper("fragment-only", scraper.ScrapeTypeFragment),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cache := &recordingPerformerScraperCache{}
			if tt.available != nil {
				cache.scrapers = []*scraper.Scraper{tt.available}
			}
			j := &analyzeSceneMetadataJob{
				input: AnalyzeSceneMetadataInput{
					PerformerVerifierScraperIDs: []string{tt.selectedID},
				},
				scraperCache: cache,
			}
			if tt.selectedID == "" {
				j.input.PerformerVerifierScraperIDs = nil
			}
			j.resolvePerformerVerifierScrapers()

			created, err := j.verifyAndCreatePerformers(context.Background(), []string{"Jane Doe"})
			if err != nil {
				t.Fatalf("verifyAndCreatePerformers() error = %v", err)
			}
			if len(created) != 0 {
				t.Fatalf("created IDs = %#v, want none", created)
			}
			if len(cache.calls) != 0 {
				t.Fatalf("scraper calls = %#v, want none", cache.calls)
			}
		})
	}
}

func TestScrapeVerifyPerformerStopsAfterExactMatch(t *testing.T) {
	otherName := "Janet Doe"
	confirmedName := "jAnE dOe"
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{
			performerScraper("first", scraper.ScrapeTypeName),
			performerScraper("second", scraper.ScrapeTypeName),
			performerScraper("third", scraper.ScrapeTypeName),
		},
		responses: map[performerScrapeCall][]scraper.ScrapedContent{
			{scraperID: "first", candidate: "Jane Doe"}: {
				&models.ScrapedPerformer{Name: &otherName},
			},
			{scraperID: "second", candidate: "Jane Doe"}: {
				&models.ScrapedPerformer{Name: &confirmedName},
			},
		},
	}
	j := &analyzeSceneMetadataJob{
		input: AnalyzeSceneMetadataInput{
			PerformerVerifierScraperIDs: []string{"first", "second", "third"},
		},
		scraperCache: cache,
	}
	j.resolvePerformerVerifierScrapers()

	got, err := j.scrapeVerifyPerformer(context.Background(), "Jane Doe")
	if err != nil {
		t.Fatalf("scrapeVerifyPerformer() error = %v", err)
	}
	if got != confirmedName {
		t.Fatalf("scrapeVerifyPerformer() = %q, want %q", got, confirmedName)
	}
	wantCalls := []performerScrapeCall{
		{scraperID: "first", candidate: "Jane Doe"},
		{scraperID: "second", candidate: "Jane Doe"},
	}
	if !reflect.DeepEqual(cache.calls, wantCalls) {
		t.Fatalf("scraper calls = %#v, want %#v", cache.calls, wantCalls)
	}
}

func TestVerifyAndCreatePerformersNoMatchCallBound(t *testing.T) {
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{
			performerScraper("first", scraper.ScrapeTypeName),
			performerScraper("second", scraper.ScrapeTypeName),
		},
	}
	j := &analyzeSceneMetadataJob{
		input: AnalyzeSceneMetadataInput{
			PerformerVerifierScraperIDs: []string{"second", "first"},
		},
		scraperCache: cache,
	}
	j.resolvePerformerVerifierScrapers()

	candidates := []string{"Alice Smith", "Betty Jones", "Carol Evans"}
	created, err := j.verifyAndCreatePerformers(context.Background(), candidates)
	if err != nil {
		t.Fatalf("verifyAndCreatePerformers() error = %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("created IDs = %#v, want none", created)
	}
	wantCalls := []performerScrapeCall{
		{scraperID: "second", candidate: "Alice Smith"},
		{scraperID: "first", candidate: "Alice Smith"},
		{scraperID: "second", candidate: "Betty Jones"},
		{scraperID: "first", candidate: "Betty Jones"},
		{scraperID: "second", candidate: "Carol Evans"},
		{scraperID: "first", candidate: "Carol Evans"},
	}
	if !reflect.DeepEqual(cache.calls, wantCalls) {
		t.Fatalf("scraper calls = %#v, want %#v", cache.calls, wantCalls)
	}
}

func TestVerifyAndCreatePerformersDryRunSkipsScrapers(t *testing.T) {
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{
			performerScraper("name", scraper.ScrapeTypeName),
		},
	}
	j := &analyzeSceneMetadataJob{
		input: AnalyzeSceneMetadataInput{
			DryRun:                      true,
			PerformerVerifierScraperIDs: []string{"name"},
		},
		scraperCache: cache,
	}
	j.resolvePerformerVerifierScrapers()

	created, err := j.verifyAndCreatePerformers(context.Background(), []string{"Jane Doe"})
	if err != nil {
		t.Fatalf("verifyAndCreatePerformers() error = %v", err)
	}
	if len(created) != 0 {
		t.Fatalf("created IDs = %#v, want none", created)
	}
	if len(cache.calls) != 0 {
		t.Fatalf("scraper calls = %#v, want none", cache.calls)
	}
}

func TestVerifyAndCreatePerformersStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{
			performerScraper("canceling-scraper", scraper.ScrapeTypeName),
		},
		cancel:      cancel,
		cancelAfter: 1,
	}
	j := &analyzeSceneMetadataJob{
		input: AnalyzeSceneMetadataInput{
			PerformerVerifierScraperIDs: []string{"canceling-scraper"},
		},
		scraperCache: cache,
	}
	j.resolvePerformerVerifierScrapers()

	_, err := j.verifyAndCreatePerformers(ctx, []string{"First Candidate", "Second Candidate"})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("verifyAndCreatePerformers() error = %v, want context.Canceled", err)
	}
	if len(cache.calls) != 1 {
		t.Fatalf("scraper calls = %d, want 1", len(cache.calls))
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
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{
			performerScraper("canceling-scraper", scraper.ScrapeTypeName),
		},
		cancel:      cancel,
		cancelAfter: 1,
	}
	j := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{
			PerformerVerifierScraperIDs: []string{"canceling-scraper"},
		},
		scraperCache: cache,
	}

	if err := j.Execute(ctx, &job.Progress{}); err != nil {
		t.Fatalf("Execute() error = %v, want nil for canceled job", err)
	}
	if len(cache.calls) != 1 {
		t.Fatalf("scraper calls = %d, want 1", len(cache.calls))
	}
	if cache.listCalls != 1 {
		t.Fatalf("ListScrapers() calls = %d, want 1", cache.listCalls)
	}
}

func TestPerformerLookupThreshold(t *testing.T) {
	explicit := 0.8
	belowFloor := 0.1

	tests := []struct {
		name      string
		threshold *float64
		want      float64
	}{
		{
			name: "default confidence threshold",
			want: defaultPerformerConfidenceThreshold,
		},
		{
			name:      "explicit confidence threshold",
			threshold: &explicit,
			want:      explicit,
		},
		{
			name:      "candidate plausibility safety floor",
			threshold: &belowFloor,
			want:      minCandidatePlausibility,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := &analyzeSceneMetadataJob{
				input: AnalyzeSceneMetadataInput{
					PerformerConfidenceThreshold: tt.threshold,
				},
			}
			if got := j.performerLookupThreshold(); got != tt.want {
				t.Fatalf("performerLookupThreshold() = %v, want %v", got, tt.want)
			}
		})
	}
}
