package manager

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/stashapp/stash/pkg/models"
	"github.com/stashapp/stash/pkg/scraper"
)

func createTestStudio(t testing.TB, r models.Repository, name string, aliases, urls []string) int {
	t.Helper()
	studio := models.NewStudio()
	studio.Name = name
	studio.Aliases = models.NewRelatedStrings(aliases)
	studio.URLs = models.NewRelatedStrings(urls)
	if err := r.WithTxn(context.Background(), func(ctx context.Context) error {
		return r.Studio.Create(ctx, &models.CreateStudioInput{Studio: &studio})
	}); err != nil {
		t.Fatalf("creating studio %q: %v", name, err)
	}
	return studio.ID
}

func studioScraper(id string, scrapeTypes ...scraper.ScrapeType) *scraper.Scraper {
	return &scraper.Scraper{ID: id, Name: id, Studio: &scraper.ScraperSpec{SupportedScrapes: scrapeTypes}}
}

func bravoFilms() *models.ScrapedStudio {
	return &models.ScrapedStudio{Name: "Bravo Films"}
}

func acmeProductions() *models.ScrapedStudio {
	return &models.ScrapedStudio{Name: "Acme Productions"}
}

type recordingStashBoxStudioQuerier struct {
	mu        sync.Mutex
	calls     []string
	responses []*models.ScrapedStudio
	err       error
}

func (q *recordingStashBoxStudioQuerier) FindStudio(_ context.Context, name string) (*models.ScrapedStudio, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls = append(q.calls, name)
	if q.err != nil {
		return nil, q.err
	}
	if len(q.responses) == 0 {
		return nil, nil
	}
	return q.responses[0], nil
}

// fakeStudioCompleter implements structuredTextCompleter for the
// studio provider-choice call. It decodes a raw JSON response into
// the studioProviderChoiceResponse target, returning an error that
// extractStudioProviderChoice will classify as "invalid" when the
// model produced a syntactically fine but semantically rejected
// answer. We use the "malformed" marker from
// isInvalidContextCompletionError so the test mirrors what the real
// completer would do when a strict-schema response is rejected.
type fakeStudioCompleter struct {
	mu       sync.Mutex
	calls    int
	raw      string
	err      error
	knownIDs map[string]bool
	rejected bool
}

func (f *fakeStudioCompleter) CompleteJSON(_ context.Context, _, _ string, _ string, _ json.RawMessage, _ int, target any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return f.err
	}
	if f.raw == "" {
		return errors.New("fakeStudioCompleter: no raw response set")
	}
	v, ok := target.(*studioProviderChoiceResponse)
	if !ok {
		return errors.New("unexpected target type")
	}
	if err := json.Unmarshal([]byte(f.raw), v); err != nil {
		return err
	}
	if v.ProviderID != "" {
		if f.rejected {
			return errors.New("malformed response: rejected by guard")
		}
		if !f.knownIDs[v.ProviderID] {
			return errors.New("malformed response: unknown provider_id")
		}
	}
	return nil
}

func (f *fakeStudioCompleter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// conflictingProviderJob builds a job whose two Stash-box providers
// return scrapes that match a common candidate ("Bravo Films Acme
// Productions" is a token-superset of both scrapes, satisfying
// studioCandidateMatchesScraped in both directions). The LLM may
// or may not be enabled depending on the calling test.
func conflictingProviderJob(t *testing.T, enableLLM bool) (*analyzeSceneMetadataJob, *recordingStashBoxStudioQuerier, *recordingStashBoxStudioQuerier, *fakeStudioCompleter) {
	t.Helper()
	r := newTestRepository(t)
	q1 := &recordingStashBoxStudioQuerier{responses: []*models.ScrapedStudio{bravoFilms()}}
	q2 := &recordingStashBoxStudioQuerier{responses: []*models.ScrapedStudio{acmeProductions()}}
	completer := &fakeStudioCompleter{
		knownIDs: map[string]bool{
			"stashbox:https://one.example/graphql": true,
			"stashbox:https://two.example/graphql": true,
		},
	}
	job := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{
			StudioVerifierStashBoxEndpoints:   []string{"https://one.example/graphql", "https://two.example/graphql"},
			UseLocalAIStudioProviderSelection: enableLLM,
		},
		configuredStashBoxes: []*models.StashBox{
			{Name: "One", Endpoint: "https://one.example/graphql"},
			{Name: "Two", Endpoint: "https://two.example/graphql"},
		},
		studioVerifierStashBoxes: []studioStashBoxVerifier{
			{ID: "stashbox:https://one.example/graphql", client: q1},
			{ID: "stashbox:https://two.example/graphql", client: q2},
		},
		completer: completer,
	}
	return job, q1, q2, completer
}

const conflictingCandidate = "Bravo Films Acme Productions"

func TestResolveStudioVerifierStashBoxesPreservesConfiguredSelection(t *testing.T) {
	job := &analyzeSceneMetadataJob{
		input: AnalyzeSceneMetadataInput{StudioVerifierStashBoxEndpoints: []string{
			"https://second.example/graphql", "https://missing.example/graphql",
			"https://first.example/graphql", "https://second.example/graphql",
		}},
		configuredStashBoxes: []*models.StashBox{
			{Name: "First", Endpoint: "https://first.example/graphql"},
			{Name: "Second", Endpoint: "https://second.example/graphql"},
		},
	}
	job.resolveStudioVerifierStashBoxes()
	got := make([]string, 0, len(job.studioVerifierStashBoxes))
	for _, selected := range job.studioVerifierStashBoxes {
		got = append(got, selected.ID)
	}
	want := []string{"stashbox:https://second.example/graphql", "stashbox:https://first.example/graphql"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved Stash-box verifier IDs = %#v, want %#v", got, want)
	}
}

func TestResolveStudioVerifierScrapersPreservesRequestedOrder(t *testing.T) {
	cache := &recordingPerformerScraperCache{scrapers: []*scraper.Scraper{
		studioScraper("first", scraper.ScrapeTypeName),
		studioScraper("second", scraper.ScrapeTypeName),
		studioScraper("url-only", scraper.ScrapeTypeURL),
		studioScraper("hybrid", scraper.ScrapeTypeURL, scraper.ScrapeTypeName),
	}}
	job := &analyzeSceneMetadataJob{
		input:        AnalyzeSceneMetadataInput{StudioVerifierScraperIDs: []string{"second", "missing", "url-only", "hybrid", "first", "second"}},
		scraperCache: cache,
	}
	job.resolveStudioVerifierScrapers()
	var got []string
	for _, selected := range job.studioVerifierScrapers {
		got = append(got, selected.ID)
	}
	want := []string{"second", "hybrid", "first"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved scraper IDs = %#v, want %#v", got, want)
	}
}

func TestResolveStudioCandidateExactLibraryMatch(t *testing.T) {
	r := newTestRepository(t)
	id := createTestStudio(t, r, "Example Studio", []string{"ExampleStudios"}, nil)
	job := &analyzeSceneMetadataJob{repository: r, input: AnalyzeSceneMetadataInput{}}
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, "Example Studio", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionExisting || resolution.StudioID != id ||
		resolution.Reason != studioReasonSingleExactLibraryMatch {
		t.Fatalf("resolution = %+v", resolution)
	}
}

func TestResolveStudioCandidateAliasMatch(t *testing.T) {
	r := newTestRepository(t)
	id := createTestStudio(t, r, "Example Studio", []string{"ExampleStudios"}, nil)
	job := &analyzeSceneMetadataJob{repository: r, input: AnalyzeSceneMetadataInput{}}
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, "ExampleStudios", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionExisting || resolution.StudioID != id {
		t.Fatalf("resolution = %+v", resolution)
	}
}

// Single configured provider returns a usable result - the resolver
// should adopt it without involving the LLM.
func TestResolveStudioCandidateSingleProviderAdopts(t *testing.T) {
	r := newTestRepository(t)
	querier := &recordingStashBoxStudioQuerier{responses: []*models.ScrapedStudio{bravoFilms()}}
	job := &analyzeSceneMetadataJob{
		repository: r,
		input:      AnalyzeSceneMetadataInput{StudioVerifierStashBoxEndpoints: []string{"https://box.example/graphql"}},
		configuredStashBoxes: []*models.StashBox{
			{Name: "SingleBox", Endpoint: "https://box.example/graphql"},
		},
		studioVerifierStashBoxes: []studioStashBoxVerifier{{ID: "stashbox:https://box.example/graphql", client: querier}},
	}
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, "Bravo Films", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionCreated {
		t.Fatalf("resolution = %+v, want created", resolution)
	}
	if resolution.ProviderID != "stashbox:https://box.example/graphql" {
		t.Fatalf("provider_id = %q, want stashbox", resolution.ProviderID)
	}
	if len(querier.calls) != 1 {
		t.Fatalf("querier calls = %d, want 1", len(querier.calls))
	}
}

// Multiple providers that all return the same canonical name
// should be adopted without involving the LLM.
func TestResolveStudioCandidateMultipleProvidersAgree(t *testing.T) {
	r := newTestRepository(t)
	querier1 := &recordingStashBoxStudioQuerier{responses: []*models.ScrapedStudio{bravoFilms()}}
	querier2 := &recordingStashBoxStudioQuerier{responses: []*models.ScrapedStudio{bravoFilms()}}
	completer := &fakeStudioCompleter{
		knownIDs: map[string]bool{"stashbox:https://one.example/graphql": true, "stashbox:https://two.example/graphql": true},
	}
	job := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{
			StudioVerifierStashBoxEndpoints:   []string{"https://one.example/graphql", "https://two.example/graphql"},
			UseLocalAIStudioProviderSelection: true,
		},
		configuredStashBoxes: []*models.StashBox{
			{Name: "One", Endpoint: "https://one.example/graphql"},
			{Name: "Two", Endpoint: "https://two.example/graphql"},
		},
		studioVerifierStashBoxes: []studioStashBoxVerifier{
			{ID: "stashbox:https://one.example/graphql", client: querier1},
			{ID: "stashbox:https://two.example/graphql", client: querier2},
		},
		completer: completer,
	}
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, "Bravo Films", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionCreated {
		t.Fatalf("resolution = %+v, want created", resolution)
	}
	if completer.callCount() != 0 {
		t.Fatalf("LLM should not be called when providers agree: calls=%d", completer.callCount())
	}
}

// Multiple providers that disagree (different canonical names for
// the same candidate). LLM mode off: the result is unverified.
func TestResolveStudioCandidateProvidersConflictWithoutLLM(t *testing.T) {
	job, _, _, completer := conflictingProviderJob(t, false)
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, conflictingCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionUnverified ||
		resolution.Reason != studioReasonProviderResultsConflict {
		t.Fatalf("resolution = %+v, want unverified/provider_results_conflict", resolution)
	}
	if completer.callCount() != 0 {
		t.Fatalf("LLM should not be called when disabled: calls=%d", completer.callCount())
	}
}

// Conflict + LLM mode: the LLM picks one of the conflicting
// providers and we adopt that.
func TestResolveStudioCandidateLLMDisambiguatesConflict(t *testing.T) {
	job, _, _, completer := conflictingProviderJob(t, true)
	completer.raw = `{"provider_id":"stashbox:https://two.example/graphql"}`
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, conflictingCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionCreated {
		t.Fatalf("resolution = %+v, want created", resolution)
	}
	if resolution.ProviderID != "stashbox:https://two.example/graphql" {
		t.Fatalf("provider_id = %q, want two.example (LLM's pick)", resolution.ProviderID)
	}
	if completer.callCount() != 1 {
		t.Fatalf("LLM should be called once for the conflict: calls=%d", completer.callCount())
	}
}

func TestResolveStudioCandidateLLMRejectsUnknownID(t *testing.T) {
	job, _, _, completer := conflictingProviderJob(t, true)
	completer.raw = `{"provider_id":"stashbox:https://not-in-pool.example/graphql"}`
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, conflictingCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionUnverified || resolution.Reason != studioReasonLocalAIInvalid {
		t.Fatalf("resolution = %+v, want unverified/local_ai_invalid", resolution)
	}
}

func TestResolveStudioCandidateLLMRejectsEmpty(t *testing.T) {
	job, _, _, completer := conflictingProviderJob(t, true)
	completer.raw = `{"provider_id":""}`
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, conflictingCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionUnverified || resolution.Reason != studioReasonLocalAIRejected {
		t.Fatalf("resolution = %+v, want unverified/local_ai_rejected", resolution)
	}
}

func TestResolveStudioCandidateLLMUnavailableFallsBackToUnverifiedRuntimeError(t *testing.T) {
	job, _, _, completer := conflictingProviderJob(t, true)
	completer.err = errors.New("runtime unavailable")
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, conflictingCandidate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionUnverified || resolution.Reason != studioReasonLocalAIUnavailable {
		t.Fatalf("resolution = %+v, want unverified/local_ai_unavailable", resolution)
	}
}

// Single provider, no usable result. The LLM is not consulted.
func TestResolveStudioCandidateProviderReturnsNilIsUnverified(t *testing.T) {
	r := newTestRepository(t)
	querier := &recordingStashBoxStudioQuerier{responses: nil}
	completer := &fakeStudioCompleter{
		knownIDs: map[string]bool{"stashbox:https://one.example/graphql": true},
	}
	job := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{
			StudioVerifierStashBoxEndpoints:   []string{"https://one.example/graphql"},
			UseLocalAIStudioProviderSelection: true,
		},
		studioVerifierStashBoxes: []studioStashBoxVerifier{{ID: "stashbox:https://one.example/graphql", client: querier}},
		completer:                completer,
	}
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, "Some Other Studio", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionUnverified || resolution.Reason != studioReasonProviderNoResult {
		t.Fatalf("resolution = %+v, want unverified/provider_no_result", resolution)
	}
	if completer.callCount() != 0 {
		t.Fatalf("LLM must not be called when no provider returns a usable result: calls=%d", completer.callCount())
	}
}

// Single provider returns a studio whose name doesn't match the
// candidate. The fan-out sees no usable match and the LLM is not
// consulted.
func TestResolveStudioCandidateProviderReturnsWrongNameRejected(t *testing.T) {
	r := newTestRepository(t)
	wrong := &models.ScrapedStudio{Name: "Totally Unrelated Studio"}
	querier := &recordingStashBoxStudioQuerier{responses: []*models.ScrapedStudio{wrong}}
	completer := &fakeStudioCompleter{
		knownIDs: map[string]bool{"stashbox:https://one.example/graphql": true},
	}
	job := &analyzeSceneMetadataJob{
		repository: r,
		input: AnalyzeSceneMetadataInput{
			StudioVerifierStashBoxEndpoints:   []string{"https://one.example/graphql"},
			UseLocalAIStudioProviderSelection: true,
		},
		studioVerifierStashBoxes: []studioStashBoxVerifier{{ID: "stashbox:https://one.example/graphql", client: querier}},
		completer:                completer,
	}
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, "Bravo Films", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionUnverified || resolution.Reason != studioReasonProviderNoResult {
		t.Fatalf("resolution = %+v, want unverified/provider_no_result", resolution)
	}
	if completer.callCount() != 0 {
		t.Fatalf("LLM must not be called when no usable match: calls=%d", completer.callCount())
	}
}

func TestStudioCandidateMatchesScraped(t *testing.T) {
	for _, tc := range []struct {
		name      string
		candidate string
		scraped   *models.ScrapedStudio
		want      bool
	}{
		{name: "exact name", candidate: "Bravo Films", scraped: &models.ScrapedStudio{Name: "Bravo Films"}, want: true},
		{name: "name case-insensitive", candidate: "bravo films", scraped: &models.ScrapedStudio{Name: "Bravo Films"}, want: true},
		{name: "candidate is a strict subset of scraped tokens", candidate: "Bravo", scraped: &models.ScrapedStudio{Name: "Bravo Films"}, want: true},
		{name: "scraped is a strict subset of candidate tokens", candidate: "Bravo Films Plus", scraped: &models.ScrapedStudio{Name: "Bravo Films"}, want: true},
		{name: "disjoint tokens rejected", candidate: "Bravo Films", scraped: &models.ScrapedStudio{Name: "Acme Productions"}, want: false},
		{name: "nil scraped", candidate: "Bravo Films", scraped: nil, want: false},
		{name: "empty candidate", candidate: "", scraped: &models.ScrapedStudio{Name: "Bravo Films"}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := studioCandidateMatchesScraped(tc.candidate, tc.scraped); got != tc.want {
				t.Fatalf("studioCandidateMatchesScraped(%q, %+v) = %v, want %v", tc.candidate, tc.scraped, got, tc.want)
			}
		})
	}
}

func TestResolveStudioCandidateDryRun(t *testing.T) {
	r := newTestRepository(t)
	job := &analyzeSceneMetadataJob{
		repository: r,
		input:      AnalyzeSceneMetadataInput{DryRun: true},
	}
	resolution, err := job.resolveStudioCandidate(context.Background(), 1, "Anything", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Status != studioResolutionDryRun {
		t.Fatalf("status = %s, want dry_run", resolution.Status)
	}
}
