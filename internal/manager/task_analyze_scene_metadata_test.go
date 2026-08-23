package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/job"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scene/metadata"
	"github.com/stashapp/stash/pkg/scraper"
)

type performerScrapeCall struct {
	scraperID string
	candidate string
}

type recordingPerformerScraperCache struct {
	mu        sync.Mutex
	scrapers  []*scraper.Scraper
	listCalls int
	calls     []performerScrapeCall
	responses map[performerScrapeCall][]scraper.ScrapedContent
	errors    map[performerScrapeCall]error
	hook      func(context.Context, performerScrapeCall) error
}

func (c *recordingPerformerScraperCache) ListScrapers(_ []scraper.ScrapeContentType) []*scraper.Scraper {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.listCalls++
	return c.scrapers
}

func (c *recordingPerformerScraperCache) ScrapeName(ctx context.Context, scraperID, candidate string, _ scraper.ScrapeContentType) ([]scraper.ScrapedContent, error) {
	call := performerScrapeCall{scraperID: scraperID, candidate: candidate}
	c.mu.Lock()
	c.calls = append(c.calls, call)
	response, responseErr := c.responses[call], c.errors[call]
	hook := c.hook
	c.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, call); err != nil {
			return nil, err
		}
	}
	return response, responseErr
}

func (c *recordingPerformerScraperCache) recordedCalls() []performerScrapeCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]performerScrapeCall(nil), c.calls...)
}

type recordingStashBoxPerformerQuerier struct {
	calls     []string
	responses []*models.ScrapedPerformer
	err       error
}

func (q *recordingStashBoxPerformerQuerier) QueryPerformer(_ context.Context, candidate string) ([]*models.ScrapedPerformer, error) {
	q.calls = append(q.calls, candidate)
	return q.responses, q.err
}

func performerScraper(id string, scrapeTypes ...scraper.ScrapeType) *scraper.Scraper {
	return &scraper.Scraper{ID: id, Performer: &scraper.ScraperSpec{SupportedScrapes: scrapeTypes}}
}

func stringPointer(value string) *string { return &value }

func scrapedPerformer(name string, mutate func(*models.ScrapedPerformer)) *models.ScrapedPerformer {
	ret := &models.ScrapedPerformer{Name: stringPointer(name)}
	if mutate != nil {
		mutate(ret)
	}
	return ret
}

func newResolutionJob(r models.Repository, input AnalyzeSceneMetadataInput, cache *recordingPerformerScraperCache) *analyzeSceneMetadataJob {
	job := &analyzeSceneMetadataJob{repository: r, input: input, scraperCache: cache}
	job.resolvePerformerVerifierScrapers()
	return job
}

func createTestPerformer(t testing.TB, r models.Repository, name, disambiguation, birthdate string, urls, aliases []string) int {
	t.Helper()
	performer := models.NewPerformer()
	performer.Name = name
	performer.Disambiguation = disambiguation
	performer.URLs = models.NewRelatedStrings(urls)
	performer.Aliases = models.NewRelatedStrings(aliases)
	if birthdate != "" {
		parsed, err := models.ParseDate(birthdate)
		if err != nil {
			t.Fatal(err)
		}
		performer.Birthdate = &parsed
	}
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		return r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &performer})
	}); err != nil {
		t.Fatalf("creating performer %q: %v", name, err)
	}
	return performer.ID
}

func createTestPerformerWithStashID(t testing.TB, r models.Repository, name, endpoint, stashID string) int {
	t.Helper()
	performer := models.NewPerformer()
	performer.Name = name
	performer.StashIDs = models.NewRelatedStashIDs([]models.StashID{{
		Endpoint: endpoint,
		StashID:  stashID,
	}})
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		return r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &performer})
	}); err != nil {
		t.Fatalf("creating performer %q: %v", name, err)
	}
	return performer.ID
}

func createTestScene(t testing.TB, r models.Repository, title string) *models.Scene {
	t.Helper()
	scene := models.NewScene()
	scene.Title = title
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		return r.Scene.Create(ctx, &scene, nil)
	}); err != nil {
		t.Fatalf("creating scene %q: %v", title, err)
	}
	return &scene
}

func scenePerformerIDs(t testing.TB, r models.Repository, sceneID int) []int {
	t.Helper()
	var ids []int
	if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
		scene, err := r.Scene.Find(ctx, sceneID)
		if err != nil {
			return err
		}
		if err := scene.LoadPerformerIDs(ctx, r.Scene); err != nil {
			return err
		}
		ids = append(ids, scene.PerformerIDs.List()...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return ids
}

func performerCount(t testing.TB, r models.Repository) int {
	t.Helper()
	var count int
	if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		count, err = r.Performer.Count(ctx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestResolvePerformerVerifierScrapersPreservesRequestedOrder(t *testing.T) {
	cache := &recordingPerformerScraperCache{scrapers: []*scraper.Scraper{
		performerScraper("first", scraper.ScrapeTypeName),
		performerScraper("second", scraper.ScrapeTypeName),
		performerScraper("url-only", scraper.ScrapeTypeURL),
		performerScraper("hybrid", scraper.ScrapeTypeURL, scraper.ScrapeTypeName),
	}}
	job := &analyzeSceneMetadataJob{
		input:        AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"second", "missing", "url-only", "hybrid", "first", "second"}},
		scraperCache: cache,
	}
	job.resolvePerformerVerifierScrapers()
	var got []string
	for _, selected := range job.performerVerifierScrapers {
		got = append(got, selected.ID)
	}
	if want := []string{"second", "hybrid", "first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved scraper IDs = %#v, want %#v", got, want)
	}
}

func TestResolvePerformerVerifierStashBoxesPreservesConfiguredSelection(t *testing.T) {
	job := &analyzeSceneMetadataJob{
		input: AnalyzeSceneMetadataInput{PerformerVerifierStashBoxEndpoints: []string{
			"https://second.example/graphql", "https://missing.example/graphql",
			"https://first.example/graphql", "https://second.example/graphql",
		}},
		configuredStashBoxes: []*models.StashBox{
			{Name: "First", Endpoint: "https://first.example/graphql"},
			{Name: "Second", Endpoint: "https://second.example/graphql"},
		},
	}
	job.resolvePerformerVerifierStashBoxes()
	got := make([]string, 0, len(job.performerVerifierStashBoxes))
	for _, selected := range job.performerVerifierStashBoxes {
		got = append(got, selected.ID)
	}
	want := []string{"stashbox:https://second.example/graphql", "stashbox:https://first.example/graphql"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved Stash-box verifier IDs = %#v, want %#v", got, want)
	}
}

func TestAnalyzeSceneMetadataPerformerResolution(t *testing.T) {
	t.Run("live exact alias fast path", func(t *testing.T) {
		r := newTestRepository(t)
		id := createTestPerformer(t, r, "Canonical Name", "", "", nil, []string{"Jane Doe"})
		cache := &recordingPerformerScraperCache{scrapers: []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)}}
		job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"name"}}, cache)
		resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"jane doe", "Jane-Doe"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(resolutions) != 1 || resolutions[0].Status != performerResolutionExisting || resolutions[0].PerformerID != id || resolutions[0].Reason != performerReasonSingleExactLibraryMatch {
			t.Fatalf("resolution = %+v", resolutions)
		}
		if calls := cache.recordedCalls(); len(calls) != 0 {
			t.Fatalf("fast path scraper calls = %+v", calls)
		}
	})

	t.Run("wildcards are re-filtered", func(t *testing.T) {
		r := newTestRepository(t)
		createTestPerformer(t, r, "Jane X Doe", "", "", nil, nil)
		var identities []performerIdentity
		if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
			var err error
			identities, err = findExactPerformerIdentities(ctx, r.Performer, "Jane % Doe")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if len(identities) != 0 {
			t.Fatalf("wildcard lookup returned %+v", identities)
		}
	})

	t.Run("URL normalization is strict", func(t *testing.T) {
		got, ok := normalizeProfileURL("HTTPS://EXAMPLE.COM/Profile/Alice/#fragment")
		if !ok || got != "https://example.com/Profile/Alice" {
			t.Fatalf("normalized URL = %q/%v", got, ok)
		}
		if _, ok := normalizeProfileURL("example.com/Alice"); ok {
			t.Fatal("relative profile URL was accepted")
		}
	})
}

func TestScrapeVerifyPerformersCollectsOrderedResultsAndFailures(t *testing.T) {
	other := "Janet Doe"
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{
			performerScraper("first", scraper.ScrapeTypeName),
			performerScraper("second", scraper.ScrapeTypeName),
			performerScraper("third", scraper.ScrapeTypeName),
		},
		responses: map[performerScrapeCall][]scraper.ScrapedContent{
			{scraperID: "first", candidate: "Jane Doe"}: {scrapedPerformer("jAnE dOe", nil), &models.ScrapedPerformer{Name: &other}},
			{scraperID: "third", candidate: "Jane Doe"}: {scrapedPerformer("Jane Doe", nil)},
		},
		errors: map[performerScrapeCall]error{{scraperID: "second", candidate: "Jane Doe"}: errors.New("offline")},
	}
	job := newResolutionJob(models.Repository{}, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"first", "second", "third"}}, cache)
	verified, failures, err := job.scrapeVerifyPerformers(context.Background(), "Jane Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 2 || verified[0].ScraperID != "first" || verified[1].ScraperID != "third" {
		t.Fatalf("verified identities = %+v", verified)
	}
	if len(failures) != 1 || failures[0].ScraperID != "second" || failures[0].Err == nil {
		t.Fatalf("failures = %+v", failures)
	}
	wantCalls := []performerScrapeCall{{"first", "Jane Doe"}, {"second", "Jane Doe"}, {"third", "Jane Doe"}}
	if got := cache.recordedCalls(); !reflect.DeepEqual(got, wantCalls) {
		t.Fatalf("calls = %+v, want %+v", got, wantCalls)
	}
}

func TestScrapeVerifyPerformersIncludesStashBoxResultsAndFailures(t *testing.T) {
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{performerScraper("script", scraper.ScrapeTypeName)},
		responses: map[performerScrapeCall][]scraper.ScrapedContent{
			{scraperID: "script", candidate: "Jane Doe"}: {scrapedPerformer("Jane Doe", nil)},
		},
	}
	success := &recordingStashBoxPerformerQuerier{
		responses: []*models.ScrapedPerformer{
			scrapedPerformer("jane doe", func(p *models.ScrapedPerformer) {
				p.RemoteSiteID = stringPointer("remote-jane")
			}),
			scrapedPerformer("Janet Doe", nil),
		},
	}
	failure := &recordingStashBoxPerformerQuerier{err: errors.New("stash-box offline")}
	job := newResolutionJob(models.Repository{}, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"script"}}, cache)
	job.performerVerifierStashBoxes = []performerStashBoxVerifier{
		{ID: "stashbox:https://one.example/graphql", client: success},
		{ID: "stashbox:https://two.example/graphql", client: failure},
	}

	verified, failures, err := job.scrapeVerifyPerformers(context.Background(), "Jane Doe")
	if err != nil {
		t.Fatal(err)
	}
	if len(verified) != 2 ||
		verified[0].ScraperID != "script" ||
		verified[1].ScraperID != "stashbox:https://one.example/graphql" ||
		verified[1].RemoteSiteID != "remote-jane" {
		t.Fatalf("verified identities = %+v", verified)
	}
	if len(failures) != 1 || failures[0].ScraperID != "stashbox:https://two.example/graphql" {
		t.Fatalf("failures = %+v", failures)
	}
	if want := []string{"Jane Doe"}; !reflect.DeepEqual(success.calls, want) || !reflect.DeepEqual(failure.calls, want) {
		t.Fatalf("Stash-box calls = success:%#v failure:%#v, want %#v", success.calls, failure.calls, want)
	}
	wantIDs := []string{"script", "stashbox:https://one.example/graphql", "stashbox:https://two.example/graphql"}
	if !reflect.DeepEqual(job.lastVerifierScraperIDs, wantIDs) {
		t.Fatalf("verifier IDs = %#v, want %#v", job.lastVerifierScraperIDs, wantIDs)
	}
}

func TestAnalyzeSceneMetadataPlansPerformerFromSelectedStashBox(t *testing.T) {
	r := newTestRepository(t)
	querier := &recordingStashBoxPerformerQuerier{
		responses: []*models.ScrapedPerformer{
			scrapedPerformer("Alice Example", func(p *models.ScrapedPerformer) {
				p.RemoteSiteID = stringPointer("remote-alice")
				p.URLs = []string{"https://example.com/performers/alice"}
			}),
		},
	}
	job := &analyzeSceneMetadataJob{
		repository: r,
		performerVerifierStashBoxes: []performerStashBoxVerifier{
			{ID: "stashbox:https://one.example/graphql", client: querier},
		},
	}

	resolutions, err := job.resolvePerformerCandidates(context.Background(), 17, []string{"Alice Example"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolutions) != 1 ||
		resolutions[0].Status != performerResolutionProposed ||
		resolutions[0].Proposed == nil ||
		resolutions[0].Proposed.Name != "Alice Example" ||
		!reflect.DeepEqual(resolutions[0].ScraperIDs, []string{"stashbox:https://one.example/graphql"}) {
		t.Fatalf("resolution = %+v", resolutions)
	}
	if got := performerCount(t, r); got != 0 {
		t.Fatalf("performer count = %d, want 0 before apply", got)
	}
	if want := []string{"Alice Example"}; !reflect.DeepEqual(querier.calls, want) {
		t.Fatalf("Stash-box calls = %#v, want %#v", querier.calls, want)
	}
}

func TestScrapeVerifyPerformersCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{performerScraper("first", scraper.ScrapeTypeName), performerScraper("second", scraper.ScrapeTypeName)},
		hook: func(_ context.Context, call performerScrapeCall) error {
			if call.scraperID == "first" {
				cancel()
				return context.Canceled
			}
			return nil
		},
	}
	job := newResolutionJob(models.Repository{}, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"first", "second"}}, cache)
	_, failures, err := job.scrapeVerifyPerformers(ctx, "Jane Doe")
	if !errors.Is(err, context.Canceled) || len(failures) != 1 || failures[0].ScraperID != "first" {
		t.Fatalf("error/failures = %v/%+v", err, failures)
	}
	if got := cache.recordedCalls(); len(got) != 1 {
		t.Fatalf("calls = %+v, want one", got)
	}
}

func TestAnalyzeSceneMetadataPerformerResolutionFailures(t *testing.T) {
	t.Run("all verifiers fail", func(t *testing.T) {
		r := newTestRepository(t)
		cache := &recordingPerformerScraperCache{
			scrapers: []*scraper.Scraper{performerScraper("first", scraper.ScrapeTypeName), performerScraper("second", scraper.ScrapeTypeName)},
			errors: map[performerScrapeCall]error{
				{"first", "Jane Doe"}:  errors.New("first offline"),
				{"second", "Jane Doe"}: errors.New("second offline"),
			},
		}
		job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"first", "second"}}, cache)
		resolutions, err := job.resolvePerformerCandidates(context.Background(), 9, []string{"Jane Doe"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(resolutions) != 1 || resolutions[0].Status != performerResolutionUnverified ||
			resolutions[0].Reason != performerReasonVerifierFailed ||
			!reflect.DeepEqual(resolutions[0].ScraperIDs, []string{"first", "second"}) {
			t.Fatalf("resolution = %+v", resolutions)
		}
	})

	t.Run("parent cancellation is finalized", func(t *testing.T) {
		r := newTestRepository(t)
		ctx, cancel := context.WithCancel(context.Background())
		cache := &recordingPerformerScraperCache{
			scrapers: []*scraper.Scraper{performerScraper("first", scraper.ScrapeTypeName)},
			hook: func(context.Context, performerScrapeCall) error {
				cancel()
				return context.Canceled
			},
		}
		job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"first"}}, cache)
		resolutions, err := job.resolvePerformerCandidates(ctx, 9, []string{"Jane Doe"}, nil)
		if !errors.Is(err, context.Canceled) || len(resolutions) != 1 ||
			resolutions[0].Status != performerResolutionCancelled || resolutions[0].Reason != performerReasonCancelled {
			t.Fatalf("resolution/error = %+v / %v", resolutions, err)
		}
	})
}

func TestAnalyzeSceneMetadataSequentialReuse(t *testing.T) {
	r := newTestRepository(t)
	scenes := []*models.Scene{createTestScene(t, r, "Alice Example"), createTestScene(t, r, "Alice Example")}
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)},
		responses: map[performerScrapeCall][]scraper.ScrapedContent{
			{scraperID: "name", candidate: "Alice Example"}: {scrapedPerformer("Alice Example", func(p *models.ScrapedPerformer) {
				p.RemoteSiteID = stringPointer("remote-alice")
				p.URLs = []string{"https://example.com/alice"}
			})},
		},
	}
	threshold := 0.3
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{
		PerformerVerifierScraperIDs: []string{"name"}, PerformerConfidenceThreshold: &threshold,
	}, cache)
	if err := job.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, scene := range scenes {
		if err := job.processScene(context.Background(), scene); err != nil {
			t.Fatal(err)
		}
	}
	if got := performerCount(t, r); got != 1 {
		t.Fatalf("performer count = %d, want 1", got)
	}
	first, second := scenePerformerIDs(t, r, scenes[0].ID), scenePerformerIDs(t, r, scenes[1].ID)
	if len(first) != 1 || !reflect.DeepEqual(first, second) {
		t.Fatalf("scene performer IDs = %v / %v", first, second)
	}
	if got := cache.recordedCalls(); len(got) != 1 {
		t.Fatalf("scraper calls = %+v, want one", got)
	}
}

func TestAnalyzeSceneMetadataStaleSnapshot(t *testing.T) {
	r := newTestRepository(t)
	scene := createTestScene(t, r, "Fresh Performer")
	threshold := 0.3
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerConfidenceThreshold: &threshold}, &recordingPerformerScraperCache{})
	if err := job.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := createTestPerformer(t, r, "Fresh Performer", "", "", nil, nil)
	if err := job.processScene(context.Background(), scene); err != nil {
		t.Fatal(err)
	}
	if got := scenePerformerIDs(t, r, scene.ID); !reflect.DeepEqual(got, []int{id}) {
		t.Fatalf("scene performer IDs = %v, want [%d]", got, id)
	}
}

func TestAnalyzeSceneMetadataReplacesPerformersFromAuthoritativeSceneID(t *testing.T) {
	r := newTestRepository(t)
	const endpoint = "https://stashdb.org/graphql"
	christinaID := createTestPerformerWithStashID(t, r, "Christina Sage", endpoint, "christina")
	isaID := createTestPerformerWithStashID(t, r, "Isa Bella", endpoint, "isa")
	wrongID := createTestPerformer(t, r, "Bella Rose", "", "", nil, nil)

	scene := models.NewScene()
	scene.Title = "#132 ’s Naughty Surprise [360p]"
	scene.Details = "Isa Bella comes home with her bestie Christina Sage."
	scene.PerformerIDs = models.NewRelatedIDs([]int{wrongID, christinaID, isaID})
	scene.StashIDs = models.NewRelatedStashIDs([]models.StashID{{
		Endpoint: endpoint,
		StashID:  "35fbe26a-fce9-4a16-a30c-62889d22ee73",
	}})
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		return r.Scene.Create(ctx, &scene, nil)
	}); err != nil {
		t.Fatal(err)
	}

	job := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{
			UseDetails: true, ReplaceLocalPerformersFromRemote: true,
			ProviderPolicies: []ProviderPolicy{{
				Endpoint: endpoint, Priority: 0, PerformerMode: ProviderFieldModeReplace,
				StudioMode: ProviderFieldModeObserve, DateMode: ProviderFieldModeObserve, TitleMode: ProviderFieldModeObserve,
			}},
		},
		configuredStashBoxes: []*models.StashBox{{
			Endpoint: endpoint,
		}},
		scenePerformerLookup: func(_ context.Context, _ models.StashBox, sceneID string) ([]*models.ScrapedPerformer, error) {
			if sceneID != "35fbe26a-fce9-4a16-a30c-62889d22ee73" {
				t.Fatalf("scene lookup ID = %q", sceneID)
			}
			return []*models.ScrapedPerformer{
				scrapedPerformer("Christina Sage", func(p *models.ScrapedPerformer) {
					p.RemoteSiteID = stringPointer("christina")
				}),
				scrapedPerformer("Isa Bella", func(p *models.ScrapedPerformer) {
					p.RemoteSiteID = stringPointer("isa")
				}),
			}, nil
		},
	}

	if err := job.processScene(context.Background(), &scene); err != nil {
		t.Fatal(err)
	}
	got := scenePerformerIDs(t, r, scene.ID)
	want := []int{christinaID, isaID}
	if !sameIDSet(got, want) {
		t.Fatalf("scene performer IDs = %v, want %v", got, want)
	}
}

func TestAnalyzeSceneMetadataPreservesAuthoritativeCastWhenRemotePerformerIsMissing(t *testing.T) {
	r := newTestRepository(t)
	const endpoint = "https://stashdb.org/graphql"
	existingID := createTestPerformerWithStashID(t, r, "Existing Performer", endpoint, "existing")
	wrongID := createTestPerformer(t, r, "Wrong Performer", "", "", nil, nil)

	scene := models.NewScene()
	scene.Title = "Authoritative cast"
	scene.PerformerIDs = models.NewRelatedIDs([]int{wrongID, existingID})
	scene.StashIDs = models.NewRelatedStashIDs([]models.StashID{{
		Endpoint: endpoint,
		StashID:  "scene-id",
	}})
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		return r.Scene.Create(ctx, &scene, nil)
	}); err != nil {
		t.Fatal(err)
	}

	job := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{
			ReplaceLocalPerformersFromRemote: true,
			ProviderPolicies: []ProviderPolicy{{
				Endpoint: endpoint, Priority: 0, PerformerMode: ProviderFieldModeReplace,
				StudioMode: ProviderFieldModeObserve, DateMode: ProviderFieldModeObserve, TitleMode: ProviderFieldModeObserve,
			}},
		},
		configuredStashBoxes: []*models.StashBox{{Endpoint: endpoint}},
		scenePerformerLookup: func(context.Context, models.StashBox, string) ([]*models.ScrapedPerformer, error) {
			return []*models.ScrapedPerformer{
				scrapedPerformer("Existing Performer", func(p *models.ScrapedPerformer) {
					p.RemoteSiteID = stringPointer("existing")
				}),
				scrapedPerformer("New Performer", func(p *models.ScrapedPerformer) {
					p.RemoteSiteID = stringPointer("new")
				}),
			}, nil
		},
	}

	if err := job.processScene(context.Background(), &scene); err != nil {
		t.Fatal(err)
	}
	var matches []performerIdentity
	var stashMatches []*models.Performer
	if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		matches, err = findExactPerformerIdentities(ctx, r.Performer, "New Performer")
		if err != nil {
			return err
		}
		stashMatches, err = r.Performer.FindByStashID(ctx, models.StashID{
			Endpoint: endpoint,
			StashID:  "new",
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 || len(stashMatches) != 0 {
		t.Fatalf("planner created missing authoritative performer: names=%v stash=%v", matches, stashMatches)
	}
	got := scenePerformerIDs(t, r, scene.ID)
	want := []int{wrongID, existingID}
	if !sameIDSet(got, want) {
		t.Fatalf("scene performer IDs = %v, want preserved %v", got, want)
	}
}

func TestAnalyzeSceneMetadataProviderPriorityControlsPerformerAuthority(t *testing.T) {
	r := newTestRepository(t)
	const (
		firstEndpoint  = "https://first.example/graphql"
		secondEndpoint = "https://theporndb.example/graphql"
	)
	firstID := createTestPerformerWithStashID(t, r, "First Authority", firstEndpoint, "first-performer")
	secondID := createTestPerformerWithStashID(t, r, "Second Authority", secondEndpoint, "second-performer")
	scene := models.NewScene()
	scene.StashIDs = models.NewRelatedStashIDs([]models.StashID{
		{Endpoint: firstEndpoint, StashID: "first-scene"},
		{Endpoint: secondEndpoint, StashID: "second-scene"},
	})
	lookup := func(_ context.Context, box models.StashBox, _ string) ([]*models.ScrapedPerformer, error) {
		if box.Endpoint == firstEndpoint {
			return []*models.ScrapedPerformer{scrapedPerformer("First Authority", func(p *models.ScrapedPerformer) {
				p.RemoteSiteID = stringPointer("first-performer")
			})}, nil
		}
		return []*models.ScrapedPerformer{scrapedPerformer("Second Authority", func(p *models.ScrapedPerformer) {
			p.RemoteSiteID = stringPointer("second-performer")
		})}, nil
	}
	job := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{ProviderPolicies: []ProviderPolicy{
			{Endpoint: firstEndpoint, Priority: 0, PerformerMode: ProviderFieldModeMerge},
			{Endpoint: secondEndpoint, Priority: 1, PerformerMode: ProviderFieldModeMerge},
		}},
		configuredStashBoxes: []*models.StashBox{{Endpoint: secondEndpoint}, {Endpoint: firstEndpoint}},
		scenePerformerLookup: lookup,
	}
	first, ok := job.authoritativeScenePerformerIDs(context.Background(), &scene)
	if !ok || len(first.IDs) != 1 || first.IDs[0] != firstID {
		t.Fatalf("first authority = %+v, found %v", first, ok)
	}
	job.input.ProviderPolicies[0].Priority = 1
	job.input.ProviderPolicies[1].Priority = 0
	second, ok := job.authoritativeScenePerformerIDs(context.Background(), &scene)
	if !ok || len(second.IDs) != 1 || second.IDs[0] != secondID {
		t.Fatalf("second authority = %+v, found %v", second, ok)
	}
}

func TestAnalyzeSceneMetadataDuplicateLibrary(t *testing.T) {
	r := newTestRepository(t)
	first := createTestPerformer(t, r, "Duplicate Name", "first", "", nil, nil)
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{}, &recordingPerformerScraperCache{})
	if err := job.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	createTestPerformer(t, r, "Duplicate Name", "second", "", nil, nil)
	scene := createTestScene(t, r, "Duplicate Name")
	if err := job.processScene(context.Background(), scene); err != nil {
		t.Fatal(err)
	}
	if got := scenePerformerIDs(t, r, scene.ID); len(got) != 0 {
		t.Fatalf("stale exact ID %d was attached: %v", first, got)
	}
	if got := performerCount(t, r); got != 2 {
		t.Fatalf("performer count = %d, want 2", got)
	}
}

func TestAnalyzeSceneMetadataDistinctScraperResults(t *testing.T) {
	r := newTestRepository(t)
	scene := createTestScene(t, r, "Conflict Person")
	cache := &recordingPerformerScraperCache{scrapers: []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)}, responses: make(map[performerScrapeCall][]scraper.ScrapedContent)}
	for index := 0; index < 5; index++ {
		remoteID := fmt.Sprintf("remote-%d", index)
		profileURL := fmt.Sprintf("https://example.com/person/%d", index)
		cache.responses[performerScrapeCall{"name", "Conflict Person"}] = append(cache.responses[performerScrapeCall{"name", "Conflict Person"}],
			scrapedPerformer("Conflict Person", func(p *models.ScrapedPerformer) {
				p.RemoteSiteID = &remoteID
				p.URLs = []string{profileURL}
			}))
	}
	threshold := 0.3
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"name"}, PerformerConfidenceThreshold: &threshold}, cache)
	if err := job.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := scene.UpdatedAt
	if err := job.processScene(context.Background(), scene); err != nil {
		t.Fatal(err)
	}
	if performerCount(t, r) != 0 || len(scenePerformerIDs(t, r, scene.ID)) != 0 {
		t.Fatal("conflicting scraper identities caused a write")
	}
	var updated time.Time
	if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
		fresh, err := r.Scene.Find(ctx, scene.ID)
		if err == nil {
			updated = fresh.UpdatedAt
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !updated.Equal(before) {
		t.Fatalf("scene updated_at changed: %v -> %v", before, updated)
	}
}

func TestAnalyzeSceneMetadataStrongIdentity(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*models.ScrapedPerformer, int)
	}{
		{name: "stored ID", mutate: func(p *models.ScrapedPerformer, id int) { p.StoredID = stringPointer(fmt.Sprint(id)) }},
		{name: "profile URL", mutate: func(p *models.ScrapedPerformer, _ int) { p.URLs = []string{"HTTPS://EXAMPLE.COM/target/#bio"} }},
		{name: "disambiguation", mutate: func(p *models.ScrapedPerformer, _ int) { p.Disambiguation = stringPointer("target") }},
		{name: "birthdate", mutate: func(p *models.ScrapedPerformer, _ int) { p.Birthdate = stringPointer("1990-01-02") }},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := newTestRepository(t)
			target := createTestPerformer(t, r, "Jane Doe", "target", "1990-01-02", []string{"https://example.com/target"}, nil)
			createTestPerformer(t, r, "Jane Doe", "other", "1991-02-03", []string{"https://example.com/other"}, nil)
			result := scrapedPerformer("Jane Doe", func(p *models.ScrapedPerformer) { test.mutate(p, target) })
			cache := &recordingPerformerScraperCache{
				scrapers:  []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)},
				responses: map[performerScrapeCall][]scraper.ScrapedContent{{"name", "Jane Doe"}: {result}},
			}
			job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"name"}}, cache)
			resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Jane Doe"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(resolutions) != 1 || resolutions[0].Status != performerResolutionExisting || resolutions[0].PerformerID != target || resolutions[0].Reason != performerReasonScraperIdentityMatch {
				t.Fatalf("resolution = %+v, want existing %d", resolutions, target)
			}
		})
	}
}

func TestMatchScrapedIdentityToLibraryRejectsConflictingEvidence(t *testing.T) {
	library := []performerIdentity{
		{ID: 1, Name: "Jane Doe", Disambiguation: "one", URLs: []string{"https://example.com/one"}},
		{ID: 2, Name: "Jane Doe", Disambiguation: "two", URLs: []string{"https://example.com/two"}},
	}
	for _, test := range []struct {
		name     string
		verified []scrapedPerformerIdentity
		wantID   int
		wantOK   bool
	}{
		{
			name: "conflicting stored IDs are not broken by a URL",
			verified: []scrapedPerformerIdentity{
				{Name: "Jane Doe", StoredID: 1, URLs: []string{"https://example.com/one"}},
				{Name: "Jane Doe", StoredID: 2},
			},
		},
		{
			name: "stored ID and URL tiers disagree",
			verified: []scrapedPerformerIdentity{
				{Name: "Jane Doe", StoredID: 1},
				{Name: "Jane Doe", URLs: []string{"https://example.com/two"}},
			},
		},
		{
			name: "multiple URL matches remain ambiguous",
			verified: []scrapedPerformerIdentity{
				{Name: "Jane Doe", URLs: []string{"https://example.com/one"}},
				{Name: "Jane Doe", URLs: []string{"https://example.com/two"}},
			},
		},
		{
			name: "consistent stable evidence resolves",
			verified: []scrapedPerformerIdentity{
				{Name: "Jane Doe", StoredID: 1, URLs: []string{"https://example.com/one"}, Disambiguation: "one"},
			},
			wantID: 1,
			wantOK: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			gotID, gotOK := matchScrapedIdentityToLibrary("Jane Doe", library, test.verified)
			if gotID != test.wantID || gotOK != test.wantOK {
				t.Fatalf("matchScrapedIdentityToLibrary() = (%d, %v), want (%d, %v)", gotID, gotOK, test.wantID, test.wantOK)
			}
		})
	}
}

func TestAnalyzeSceneMetadataIndistinguishableResults(t *testing.T) {
	remote := "same-remote"
	stable := []scrapedPerformerIdentity{
		{ScraperID: "one", Name: "Jane Doe", RemoteSiteID: remote},
		{ScraperID: "one", Name: "Jane Doe", RemoteSiteID: remote},
	}
	if got := scrapedIdentityComponents(stable, nil); len(got) != 1 {
		t.Fatalf("stable identity components = %d, want 1", len(got))
	}
	sparse := []scrapedPerformerIdentity{
		{ScraperID: "one", Name: "Jane Doe", Disambiguation: "same", Birthdate: "1990-01-02"},
		{ScraperID: "two", Name: "Jane Doe", Disambiguation: "same", Birthdate: "1990-01-02"},
	}
	if got := scrapedIdentityComponents(sparse, nil); len(got) != 2 {
		t.Fatalf("sparse identity components = %d, want 2", len(got))
	}
}

func TestAnalyzeSceneMetadataConcurrentPerformerPlanningIsReadOnly(t *testing.T) {
	r := newTestRepository(t)
	verified := []scrapedPerformerIdentity{{ScraperID: "name", Name: "Concurrent Person", RemoteSiteID: "remote-person", URLs: []string{"https://example.com/concurrent"}}}
	jobs := []*analyzeSceneMetadataJob{{repository: r}, {repository: r}}
	start := make(chan struct{})
	results := make(chan performerResolution, 2)
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for _, job := range jobs {
		wait.Add(1)
		go func(job *analyzeSceneMetadataJob) {
			defer wait.Done()
			<-start
			resolution, err := job.planVerifiedPerformer(context.Background(), "Concurrent Person", verified, nil)
			results <- resolution
			errors <- err
		}(job)
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for resolution := range results {
		if resolution.Status != performerResolutionProposed || resolution.Proposed == nil {
			t.Fatalf("resolution = %+v, want proposed", resolution)
		}
	}
	if got := performerCount(t, r); got != 0 {
		t.Fatalf("performer count = %d, want 0 before apply", got)
	}
}

type fakeStructuredTextCompleter struct {
	mu       sync.Mutex
	calls    int
	response performerContextResponse
	raw      string
	err      error
}

func (f *fakeStructuredTextCompleter) CompleteJSON(_ context.Context, _, _ string, _ string, _ json.RawMessage, _ int, target any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	if f.raw != "" {
		return json.Unmarshal([]byte(f.raw), target)
	}
	value, ok := target.(*performerContextResponse)
	if !ok {
		return fmt.Errorf("unexpected target %T", target)
	}
	*value = f.response
	return nil
}

func (f *fakeStructuredTextCompleter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func TestAnalyzeSceneMetadataLocalAI(t *testing.T) {
	newAmbiguousJob := func(t *testing.T, completer structuredTextCompleter) (*analyzeSceneMetadataJob, int, int) {
		r := newTestRepository(t)
		uk := createTestPerformer(t, r, "Jane Doe", "UK", "", nil, nil)
		us := createTestPerformer(t, r, "Jane Doe", "US", "", nil, nil)
		cache := &recordingPerformerScraperCache{
			scrapers: []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)},
			responses: map[performerScrapeCall][]scraper.ScrapedContent{{"name", "Jane Doe"}: {
				scrapedPerformer("Jane Doe", func(p *models.ScrapedPerformer) {
					p.RemoteSiteID = stringPointer("uk")
					p.Disambiguation = stringPointer("UK")
				}),
				scrapedPerformer("Jane Doe", func(p *models.ScrapedPerformer) {
					p.RemoteSiteID = stringPointer("us")
					p.Disambiguation = stringPointer("US")
				}),
			}},
		}
		job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"name"}, UseLocalAIContext: true}, cache)
		job.completer = completer
		return job, uk, us
	}

	t.Run("three-way corroboration selects one duplicate", func(t *testing.T) {
		completer := &fakeStructuredTextCompleter{response: performerContextResponse{Performers: []performerContextEvidence{{Name: "Jane Doe", Disambiguation: "UK"}}}}
		job, uk, _ := newAmbiguousJob(t, completer)
		sources := []metadata.Source{metadata.NormalizeSource(metadata.Source{Kind: metadata.SourceSceneTitle, RawText: "Jane Doe (UK)"})}
		resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Jane Doe"}, sources)
		if err != nil {
			t.Fatal(err)
		}
		if len(resolutions) != 1 || resolutions[0].PerformerID != uk || resolutions[0].Status != performerResolutionExisting {
			t.Fatalf("resolution = %+v, want UK ID %d", resolutions, uk)
		}
		if completer.callCount() != 1 {
			t.Fatalf("completion calls = %d, want 1", completer.callCount())
		}
	})

	t.Run("hallucinated qualifier is rejected", func(t *testing.T) {
		completer := &fakeStructuredTextCompleter{response: performerContextResponse{Performers: []performerContextEvidence{{Name: "Jane Doe", Disambiguation: "UK"}}}}
		job, _, _ := newAmbiguousJob(t, completer)
		sources := []metadata.Source{metadata.NormalizeSource(metadata.Source{Kind: metadata.SourceSceneTitle, RawText: "Jane Doe"})}
		resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Jane Doe"}, sources)
		if err != nil {
			t.Fatal(err)
		}
		if resolutions[0].Status != performerResolutionAmbiguous || resolutions[0].Reason != performerReasonScraperResultsConflict {
			t.Fatalf("resolution = %+v", resolutions)
		}
	})

	t.Run("source-present disagreement is rejected", func(t *testing.T) {
		completer := &fakeStructuredTextCompleter{response: performerContextResponse{Performers: []performerContextEvidence{{Name: "Jane Doe", Disambiguation: "France"}}}}
		job, _, _ := newAmbiguousJob(t, completer)
		sources := []metadata.Source{metadata.NormalizeSource(metadata.Source{Kind: metadata.SourceSceneTitle, RawText: "Jane Doe France"})}
		resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Jane Doe"}, sources)
		if err != nil {
			t.Fatal(err)
		}
		if resolutions[0].Status != performerResolutionAmbiguous || resolutions[0].Reason != performerReasonScraperResultsConflict {
			t.Fatalf("resolution = %+v", resolutions)
		}
	})

	t.Run("unavailable and malformed completions are non-fatal", func(t *testing.T) {
		for name, completer := range map[string]*fakeStructuredTextCompleter{
			"unavailable": {err: errors.New("server unavailable")},
			"malformed":   {raw: "{"},
		} {
			t.Run(name, func(t *testing.T) {
				job, _, _ := newAmbiguousJob(t, completer)
				resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Jane Doe"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				wantReason := performerReasonLocalAIUnavailable
				if name == "malformed" {
					wantReason = performerReasonLocalAIInvalid
				}
				if resolutions[0].Status != performerResolutionAmbiguous || resolutions[0].Reason != wantReason {
					t.Fatalf("resolution = %+v, want reason %s", resolutions, wantReason)
				}
			})
		}
	})
}

func TestPerformerContextEvidenceBoundsAndSourceFiltering(t *testing.T) {
	long := strings.Repeat("ø", 3000)
	payload := buildPerformerContextPayload([]string{"Jane Doe"}, []metadata.Source{
		{Kind: metadata.SourceFilename, RawText: long},
		{Kind: metadata.SourceDetails, RawText: "excluded details"},
	})
	if len(payload.Sources) != 1 || len([]rune(payload.Sources[0].Text)) != 2048 {
		t.Fatalf("bounded payload sources = %+v", payload.Sources)
	}
}

func TestAnalyzeSceneMetadataDryRun(t *testing.T) {
	r := newTestRepository(t)
	createTestPerformer(t, r, "Dry Person", "one", "", nil, nil)
	createTestPerformer(t, r, "Dry Person", "two", "", nil, nil)
	scene := createTestScene(t, r, "Dry Person")
	before := scene.UpdatedAt
	cache := &recordingPerformerScraperCache{scrapers: []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)}}
	completer := &fakeStructuredTextCompleter{}
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{DryRun: true, UseLocalAIContext: true, PerformerVerifierScraperIDs: []string{"name"}}, cache)
	job.completer = completer
	if err := job.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := job.processScene(context.Background(), scene); err != nil {
		t.Fatal(err)
	}
	if len(cache.recordedCalls()) != 1 {
		t.Fatalf("dry-run scraper calls = %v, want faithful verification", cache.recordedCalls())
	}
	if performerCount(t, r) != 2 || len(scenePerformerIDs(t, r, scene.ID)) != 0 {
		t.Fatal("dry run changed performer or scene relationships")
	}
	var updated time.Time
	if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
		fresh, err := r.Scene.Find(ctx, scene.ID)
		if err == nil {
			updated = fresh.UpdatedAt
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if !updated.Equal(before) {
		t.Fatalf("dry-run updated_at changed: %v -> %v", before, updated)
	}
	var plans []models.SceneMetadataPlanRecord
	if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		plans, err = r.SceneMetadataPlan.FindSceneMetadataPlans(ctx, []int{scene.ID}, nil, nil, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || plans[0].State != string(SceneMetadataPlanProposed) {
		t.Fatalf("persisted dry-run plans = %+v, want one proposed plan", plans)
	}
}

type capturingLogger struct {
	logger.BasicLogger
	mu    sync.Mutex
	infos []string
}

func (l *capturingLogger) Infof(format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.infos = append(l.infos, fmt.Sprintf(format, args...))
}

func TestAnalyzeSceneMetadataDecisionLog(t *testing.T) {
	capture := &capturingLogger{}
	previous := logger.Logger
	logger.Logger = capture
	t.Cleanup(func() { logger.Logger = previous })
	logPerformerResolution(42, performerResolution{
		Candidate: "Jane Doe", Status: performerResolutionAmbiguous,
		MatchingIDs: []int{9, 2, 9}, ScraperIDs: []string{"zeta", "alpha", "zeta"},
		Reason: performerReasonScraperResultsConflict,
	})
	want := `[scene metadata] performer_resolution scene_id=42 candidate="Jane Doe" status="ambiguous" matching_ids=[2 9] scraper_ids=[alpha zeta] reason="scraper_results_conflict"`
	if len(capture.infos) != 1 || capture.infos[0] != want {
		t.Fatalf("captured logs = %#v, want %q", capture.infos, want)
	}
}

func TestPerformerLookupThreshold(t *testing.T) {
	explicit, belowFloor := 0.8, 0.1
	for _, test := range []struct {
		name      string
		threshold *float64
		want      float64
	}{
		{name: "default", want: defaultPerformerConfidenceThreshold},
		{name: "explicit", threshold: &explicit, want: explicit},
		{name: "floor", threshold: &belowFloor, want: minCandidatePlausibility},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := &analyzeSceneMetadataJob{input: AnalyzeSceneMetadataInput{PerformerConfidenceThreshold: test.threshold}}
			if got := job.performerLookupThreshold(); got != test.want {
				t.Fatalf("performerLookupThreshold() = %v, want %v", got, test.want)
			}
		})
	}
}

func BenchmarkResolvePerformerCandidatesExact(b *testing.B) {
	for _, size := range []int{100, 5000} {
		b.Run(fmt.Sprintf("library_%d", size), func(b *testing.B) {
			r := newTestRepository(b)
			if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
				for index := 0; index < size; index++ {
					performer := models.NewPerformer()
					performer.Name = fmt.Sprintf("Background Performer %06d", index)
					if err := r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &performer}); err != nil {
						return err
					}
				}
				performer := models.NewPerformer()
				performer.Name = "Benchmark Target"
				return r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &performer})
			}); err != nil {
				b.Fatal(err)
			}
			job := &analyzeSceneMetadataJob{repository: r}
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Benchmark Target"}, nil)
				if err != nil || len(resolutions) != 1 || resolutions[0].Status != performerResolutionExisting {
					b.Fatalf("resolution = %+v, err = %v", resolutions, err)
				}
			}
		})
	}
}

func TestAnalyzeSceneMetadataExecuteHonorsSelectedSceneIDs(t *testing.T) {
	r := newTestRepository(t)
	var selected []string
	var selectedInts []int
	for index := range 10 {
		scene := createTestScene(t, r, fmt.Sprintf("Selected Scene %d", index))
		selected = append(selected, fmt.Sprint(scene.ID))
		selectedInts = append(selectedInts, scene.ID)
	}
	excluded := createTestScene(t, r, "Excluded Scene")
	analyzerJob := newResolutionJob(r, AnalyzeSceneMetadataInput{
		DryRun: true, SceneIDs: selected,
	}, &recordingPerformerScraperCache{})
	if err := analyzerJob.Execute(context.Background(), &job.Progress{}); err != nil {
		t.Fatal(err)
	}
	var selectedPlans, excludedPlans []models.SceneMetadataPlanRecord
	if err := r.WithReadTxn(context.Background(), func(ctx context.Context) error {
		var err error
		selectedPlans, err = r.SceneMetadataPlan.FindSceneMetadataPlans(ctx, selectedInts, nil, nil, nil)
		if err != nil {
			return err
		}
		excludedPlans, err = r.SceneMetadataPlan.FindSceneMetadataPlans(ctx, []int{excluded.ID}, nil, nil, nil)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(selectedPlans) != len(selected) {
		t.Fatalf("selected plans = %d, want %d", len(selectedPlans), len(selected))
	}
	if len(excludedPlans) != 0 {
		t.Fatalf("excluded scene produced plans: %+v", excludedPlans)
	}
}

func TestLoadLibraryRecordsSkipsImplausiblePerformerNames(t *testing.T) {
	r := newTestRepository(t)
	createTestPerformer(t, r, "Anal", "", "", nil, nil)
	createTestPerformer(t, r, "Jane Doe", "", "", nil, nil)
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{}, &recordingPerformerScraperCache{})
	if err := job.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(job.performerRecords) != 1 || job.performerRecords[0].Name != "Jane Doe" {
		t.Fatalf("performerRecords = %+v, want only Jane Doe", job.performerRecords)
	}
}


func TestResolvePerformerCandidatesRejectsImplausibleNames(t *testing.T) {
	r := newTestRepository(t)
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)},
	}
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{PerformerVerifierScraperIDs: []string{"name"}}, cache)
	resolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Anal", "XXX (Bilatinmen)"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolutions) != 2 {
		t.Fatalf("resolutions = %+v", resolutions)
	}
	for _, resolution := range resolutions {
		if resolution.Status != performerResolutionUnverified || resolution.Reason != performerReasonImplausibleName ||
			resolution.Proposed != nil {
			t.Fatalf("resolution = %+v", resolution)
		}
	}
	if calls := cache.recordedCalls(); len(calls) != 0 {
		t.Fatalf("implausible names scraped: %+v", calls)
	}
}
func TestLoadLibraryRecordsIgnoresMalePerformers(t *testing.T) {
	r := newTestRepository(t)
	jane := models.NewPerformer()
	jane.Name = "Jane"
	female := models.GenderEnumFemale
	jane.Gender = &female
	john := models.NewPerformer()
	john.Name = "John"
	male := models.GenderEnumMale
	john.Gender = &male
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		if err := r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &jane}); err != nil {
			return err
		}
		return r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &john})
	}); err != nil {
		t.Fatal(err)
	}

	ignored := newResolutionJob(r, AnalyzeSceneMetadataInput{IgnoreMalePerformers: true}, &recordingPerformerScraperCache{})
	if err := ignored.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ignored.performerRecords) != 1 || ignored.performerRecords[0].Name != "Jane" {
		t.Fatalf("ignored male performerRecords = %+v", ignored.performerRecords)
	}

	included := newResolutionJob(r, AnalyzeSceneMetadataInput{}, &recordingPerformerScraperCache{})
	if err := included.loadLibraryRecords(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(included.performerRecords) != 2 {
		t.Fatalf("flag-off performerRecords = %+v, want Jane and John", included.performerRecords)
	}
}

func TestResolvePerformerCandidatesIgnoresMaleScrapedProposal(t *testing.T) {
	r := newTestRepository(t)
	male := "MALE"
	female := "FEMALE"
	cache := &recordingPerformerScraperCache{
		scrapers: []*scraper.Scraper{performerScraper("name", scraper.ScrapeTypeName)},
		responses: map[performerScrapeCall][]scraper.ScrapedContent{
			{"name", "John Doe"}: {scrapedPerformer("John Doe", func(p *models.ScrapedPerformer) {
				p.Gender = &male
			})},
			{"name", "Jane Doe"}: {scrapedPerformer("Jane Doe", func(p *models.ScrapedPerformer) {
				p.Gender = &female
			})},
			{"name", "Pat Doe"}: {scrapedPerformer("Pat Doe", nil)},
		},
	}
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{
		IgnoreMalePerformers:        true,
		PerformerVerifierScraperIDs: []string{"name"},
	}, cache)

	maleResolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"John Doe"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(maleResolutions) != 1 || maleResolutions[0].Status != performerResolutionUnverified ||
		maleResolutions[0].Proposed != nil {
		t.Fatalf("male resolution = %+v", maleResolutions)
	}

	femaleResolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Jane Doe"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(femaleResolutions) != 1 || femaleResolutions[0].Status != performerResolutionProposed ||
		femaleResolutions[0].Proposed == nil {
		t.Fatalf("female resolution = %+v", femaleResolutions)
	}

	unknownResolutions, err := job.resolvePerformerCandidates(context.Background(), 1, []string{"Pat Doe"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(unknownResolutions) != 1 || unknownResolutions[0].Status != performerResolutionProposed ||
		unknownResolutions[0].Proposed == nil {
		t.Fatalf("unknown-gender resolution = %+v", unknownResolutions)
	}
}

func TestPlanVerifiedPerformerIgnoresMaleScrapedIdentity(t *testing.T) {
	r := newTestRepository(t)
	job := newResolutionJob(r, AnalyzeSceneMetadataInput{IgnoreMalePerformers: true}, &recordingPerformerScraperCache{})
	resolution, err := job.planVerifiedPerformer(context.Background(), "John Doe", []scrapedPerformerIdentity{{
		Name: "John Doe", Gender: models.GenderEnumMale,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != performerResolutionUnverified || resolution.Reason != performerReasonIgnoredMale ||
		resolution.Proposed != nil {
		t.Fatalf("planVerifiedPerformer male = %+v", resolution)
	}
}

// Regression: ignoreMalePerformers used to be silently bypassed when a male
// library performer was the only exact-name match after the global filter
// (planVerifiedPerformer re-loaded the male identity into fresh and returned
// single_exact_library_match before checking skipMaleGender). Filter fresh
// immediately after the read txn.
func TestPlanVerifiedPerformerIgnoresMaleLibraryExactMatch(t *testing.T) {
	r := newTestRepository(t)
	male := models.GenderEnumMale
	malePerformer := models.NewPerformer()
	malePerformer.Name = "Alex Doe"
	malePerformer.Gender = &male
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		return r.Performer.Create(ctx, &models.CreatePerformerInput{Performer: &malePerformer})
	}); err != nil {
		t.Fatal(err)
	}

	job := newResolutionJob(r, AnalyzeSceneMetadataInput{IgnoreMalePerformers: true}, &recordingPerformerScraperCache{})
	female := models.GenderEnumFemale
	resolution, err := job.planVerifiedPerformer(context.Background(), "Alex Doe", []scrapedPerformerIdentity{{
		Name: "Alex Doe", Gender: female,
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != performerResolutionProposed || resolution.Reason != performerReasonVerifiedNewPerformer {
		t.Fatalf("expected proposed/verified_new_performer, got %+v", resolution)
	}
	if resolution.PerformerID != 0 {
		t.Fatalf("male library match leaked through: %+v", resolution)
	}
	if len(resolution.MatchingIDs) != 0 {
		t.Fatalf("MatchingIDs should not include filtered male library match: %+v", resolution)
	}
}
