package llamaprov

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

func TestTaxonomyAnalyzerDescribeThenVerifyResolvesStashIDs(t *testing.T) {
	ffmpeg, video := makeTaxonomyTestVideo(t)
	entries := map[string]taxonomy.Entry{
		"couch-id": {StashID: "couch-id", Canonical: "Couch", Category: "Surfaces"},
		"dildo-id": {StashID: "dildo-id", Canonical: "Dildo", Category: "Accessories"},
		"woman-id": {StashID: "woman-id", Canonical: "Woman", Category: "Roles", Aliases: []string{"woman"}},
	}
	var mu sync.Mutex
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		calls++
		content := "woman dildo couch"
		if request.ResponseFormat != nil {
			content = `{"Couch":"no","Dildo":"yes","Woman":"yes"}`
		}
		writeTextCompletion(t, w, content)
	}))
	defer server.Close()

	analyzer := newTaxonomyTestAnalyzer(t, server, ffmpeg, entries, []string{"Accessories", "Roles", "Surfaces"}, 6)
	result, err := analyzer.Analyze(context.Background(), video, aitag.Options{FrameInterval: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want describe and verify", calls)
	}
	if got := len(result.Spans["actions"]["Dildo"]); got != 1 {
		t.Fatalf("Dildo spans = %d: %#v", got, result.Spans)
	}
	if got := len(result.Spans["actions"]["Woman"]); got != 1 {
		t.Fatalf("Woman spans = %d: %#v", got, result.Spans)
	}
	stashIDs, ok := result.Models[0].Extra["stash_ids"].(map[string]string)
	if !ok || stashIDs["Dildo"] != "dildo-id" || stashIDs["Woman"] != "woman-id" {
		t.Fatalf("resolved Stash IDs = %#v", result.Models[0].Extra["stash_ids"])
	}
	if got := result.Metrics["frames"]; got != 1 {
		t.Fatalf("frames = %v", got)
	}
}

func TestTaxonomyAnalyzerFallsBackToMinimumCandidates(t *testing.T) {
	ffmpeg, video := makeTaxonomyTestVideo(t)
	entries := map[string]taxonomy.Entry{
		"a": {StashID: "a", Canonical: "Alpha", Category: "Acts"},
		"b": {StashID: "b", Canonical: "Beta", Category: "Acts"},
		"c": {StashID: "c", Canonical: "Gamma", Category: "Acts"},
		"d": {StashID: "d", Canonical: "Delta", Category: "Acts"},
	}
	var received []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ResponseFormat == nil {
			writeTextCompletion(t, w, "unrelated scenery")
			return
		}
		received = schemaRequiredLabels(t, request.ResponseFormat.JSONSchema.Schema)
		decisions := make(map[string]string, len(received))
		for _, label := range received {
			decisions[label] = "no"
		}
		encoded, _ := json.Marshal(decisions)
		writeTextCompletion(t, w, string(encoded))
	}))
	defer server.Close()

	analyzer := newTaxonomyTestAnalyzer(t, server, ffmpeg, entries, []string{"Acts"}, 3)
	if _, err := analyzer.Analyze(context.Background(), video, aitag.Options{FrameInterval: 2}, nil); err != nil {
		t.Fatal(err)
	}
	sort.Strings(received)
	if want := []string{"Alpha", "Beta", "Delta"}; !reflect.DeepEqual(received, want) {
		t.Fatalf("fallback labels = %q, want %q", received, want)
	}
}

func TestDescriptionTokenSelectsMultiwordTaxonomyName(t *testing.T) {
	analyzer := TaxonomyAnalyzer{MaxCandidates: 8, MaxPerFrame: 8}
	pool := []taxonomy.Entry{
		{StashID: "generic", Canonical: "Dildo", Category: "Accessories"},
		{StashID: "play", Canonical: "Dildo Play", Category: "Acts"},
		{StashID: "other", Canonical: "Couch", Category: "Surfaces"},
	}
	index := &taxonomy.BM25{}
	index.Rebuild(pool)
	got := analyzer.selectCandidates("A woman is holding a dildo.", pool, index)
	names := make([]string, len(got))
	for i := range got {
		names[i] = got[i].Canonical
	}
	if !reflect.DeepEqual(names, []string{"Dildo", "Dildo Play", "Couch"}) {
		t.Fatalf("candidates = %q", names)
	}
}

func TestCandidateFusionRanksDildoEntriesInLargeTaxonomy(t *testing.T) {
	pool := []taxonomy.Entry{
		{StashID: "dildo", Canonical: "Dildo", Category: "Accessories"},
		{StashID: "footjob", Canonical: "Dildo Footjob", Aliases: []string{"dildo woman"}, Category: "Acts"},
		{StashID: "couch", Canonical: "Couch", Category: "Surfaces"},
	}
	for i := range 197 {
		pool = append(pool, taxonomy.Entry{
			StashID:   fmt.Sprintf("generic-%03d", i),
			Canonical: fmt.Sprintf("Generic Placeholder %03d", i),
			Category:  "Themes",
		})
	}
	index := &taxonomy.BM25{}
	index.Rebuild(pool)
	analyzer := TaxonomyAnalyzer{MaxCandidates: 32, MaxPerFrame: 5}
	got := analyzer.selectCandidates("dildo woman", pool, index)
	names := candidateNames(got)
	if !containsString(names, "Dildo") || !containsString(names, "Dildo Footjob") {
		t.Fatalf("top five candidates = %q", names)
	}
}

func TestCandidateFusionKeepsExactAliasFirst(t *testing.T) {
	pool := []taxonomy.Entry{
		{StashID: "blowjob", Canonical: "Blowjob", Aliases: []string{"oral sex"}, Category: "Acts"},
		{StashID: "bdsm", Canonical: "BDSM", Category: "Themes"},
		{StashID: "couch", Canonical: "Couch", Category: "Surfaces"},
	}
	index := &taxonomy.BM25{}
	index.Rebuild(pool)
	analyzer := TaxonomyAnalyzer{MinCandidates: 1, MaxCandidates: 8, MaxPerFrame: 5}
	names := candidateNames(analyzer.selectCandidates("Blowjob", pool, index))
	if len(names) == 0 || names[0] != "Blowjob" {
		t.Fatalf("candidates = %q, want Blowjob first", names)
	}
	if containsString(names, "BDSM") {
		t.Fatalf("unmatched BDSM leaked into candidates: %q", names)
	}
}

func TestCandidateFusionBackfillsEmptyCaption(t *testing.T) {
	pool := []taxonomy.Entry{
		{StashID: "a", Canonical: "Alpha"},
		{StashID: "b", Canonical: "Beta"},
		{StashID: "c", Canonical: "Gamma"},
		{StashID: "d", Canonical: "Delta"},
	}
	index := &taxonomy.BM25{}
	index.Rebuild(pool)
	analyzer := TaxonomyAnalyzer{MinCandidates: 3, MaxCandidates: 8}
	names := candidateNames(analyzer.selectCandidates("", pool, index))
	if want := []string{"Alpha", "Beta", "Gamma"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("fallback = %q, want %q", names, want)
	}
}

func candidateNames(entries []taxonomy.Entry) []string {
	ret := make([]string, len(entries))
	for i := range entries {
		ret[i] = entries[i].Canonical
	}
	return ret
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestSummarizeSupportsCountsFramesAndSpans(t *testing.T) {
	end := 6.0
	supports := summarizeSupports(aitag.SpansByCategory{
		"actions": {
			"Blowjob": {
				{Start: 0, End: &end},
				{Start: 12},
			},
			"Cowgirl": {{Start: 4}},
		},
	}, 2, map[string]string{"Blowjob": "blowjob-id"})

	want := []LabelSupport{
		{Tag: "Blowjob", StashID: "blowjob-id", Frames: 5, SpanCount: 2, FirstAt: 0, LastAt: 12},
		{Tag: "Cowgirl", Frames: 1, SpanCount: 1, FirstAt: 4, LastAt: 4},
	}
	if !reflect.DeepEqual(supports, want) {
		t.Fatalf("supports = %#v, want %#v", supports, want)
	}
}

func TestSupportGateStrictAndShadow(t *testing.T) {
	spans := supportGateFixture()
	supports := summarizeSupports(spans, 2, nil)

	strict, kept, dropped := applySupportGate(spans, supports, AcceptStrict, 2, 0, 6, nil)
	if got := aitag.CountSpans(strict); got != 4 {
		t.Fatalf("strict spans = %d, want four supported Blowjob spans", got)
	}
	if len(kept) != 1 || kept[0].Tag != "Blowjob" || kept[0].Frames != 8 || kept[0].SpanCount != 4 {
		t.Fatalf("strict kept supports = %#v", kept)
	}
	if len(dropped) != 2 {
		t.Fatalf("strict dropped supports = %#v", dropped)
	}

	shadow, shadowKept, shadowDropped := applySupportGate(spans, supports, AcceptShadow, 2, 0, 6, nil)
	if got := aitag.CountSpans(shadow); got != 6 {
		t.Fatalf("shadow spans = %d, want all six", got)
	}
	if len(shadowKept) != 1 || len(shadowDropped) != 2 {
		t.Fatalf("shadow diagnostics kept=%#v dropped=%#v", shadowKept, shadowDropped)
	}
}

func TestSupportGateRescuesConfirmedSingleFrameLabel(t *testing.T) {
	spans := supportGateFixture()
	supports := summarizeSupports(spans, 2, nil)
	filtered, _, dropped := applySupportGate(
		spans,
		supports,
		AcceptRescue,
		2,
		0,
		6,
		map[string]bool{"Cowgirl": true, "Dildo Footjob": false},
	)
	if got := aitag.CountSpans(filtered); got != 5 {
		t.Fatalf("rescue spans = %d, want four Blowjob plus Cowgirl", got)
	}
	if _, ok := filtered["actions"]["Cowgirl"]; !ok {
		t.Fatal("confirmed Cowgirl was not rescued")
	}
	if _, ok := filtered["actions"]["Dildo Footjob"]; ok {
		t.Fatal("rejected Dildo Footjob survived rescue")
	}
	if len(dropped) != 1 || dropped[0].Tag != "Dildo Footjob" {
		t.Fatalf("dropped supports = %#v", dropped)
	}
}

func TestTaxonomyAnalyzerRescueReverifiesOneCandidate(t *testing.T) {
	ffmpeg, video := makeTaxonomyTestVideo(t)
	entries := map[string]taxonomy.Entry{
		"cowgirl": {StashID: "cowgirl", Canonical: "Cowgirl", Category: "Acts"},
		"dildo":   {StashID: "dildo", Canonical: "Dildo Footjob", Category: "Acts"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ResponseFormat == nil {
			writeTextCompletion(t, w, "cowgirl dildo footjob")
			return
		}
		labels := schemaRequiredLabels(t, request.ResponseFormat.JSONSchema.Schema)
		decisions := make(map[string]string, len(labels))
		for _, label := range labels {
			if len(labels) > 1 || label == "Cowgirl" {
				decisions[label] = "yes"
			} else {
				decisions[label] = "no"
			}
		}
		encoded, _ := json.Marshal(decisions)
		writeTextCompletion(t, w, string(encoded))
	}))
	defer server.Close()

	analyzer := newTaxonomyTestAnalyzer(t, server, ffmpeg, entries, []string{"Acts"}, 4)
	analyzer.AcceptMode = AcceptRescue
	analyzer.MinSupport = 2
	result, err := analyzer.Analyze(context.Background(), video, aitag.Options{FrameInterval: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := aitag.CountSpans(result.Spans); got != 1 {
		t.Fatalf("rescue result spans = %d: %#v", got, result.Spans)
	}
	if _, ok := result.Spans["actions"]["Cowgirl"]; !ok {
		t.Fatalf("Cowgirl was not rescued: %#v", result.Spans)
	}
	if _, ok := result.Spans["actions"]["Dildo Footjob"]; ok {
		t.Fatalf("Dildo Footjob survived rescue: %#v", result.Spans)
	}
}

func supportGateFixture() aitag.SpansByCategory {
	end2, end6, end10, end14 := 2.0, 6.0, 10.0, 14.0
	return aitag.SpansByCategory{
		"actions": {
			"Blowjob": {
				{Start: 0, End: &end2},
				{Start: 4, End: &end6},
				{Start: 8, End: &end10},
				{Start: 12, End: &end14},
			},
			"Cowgirl":       {{Start: 16}},
			"Dildo Footjob": {{Start: 20}},
		},
	}
}

func newTaxonomyTestAnalyzer(t *testing.T, server *httptest.Server, ffmpeg string, entries map[string]taxonomy.Entry, categories []string, maxCandidates int) *TaxonomyAnalyzer {
	t.Helper()
	provider, err := New(Config{
		Client: server.Client(), BaseURL: server.URL,
		Pair:             assets.Pair{Name: "taxonomy-test", ContextTokens: 4096},
		AllowEmptyLabels: true, FFmpegPath: ffmpeg, DefaultInterval: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return &TaxonomyAnalyzer{
		Provider: provider,
		Client: &taxonomy.Client{Cache: &taxonomy.Cache{
			Endpoint: "https://stashdb.org/graphql", UpdatedAt: time.Now().UTC(), Entries: entries,
		}},
		Categories: categories, MaxCandidates: maxCandidates, MaxPerFrame: maxCandidates,
	}
}

func writeTextCompletion(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{"message": map[string]any{"content": content}}},
	})
}

func schemaRequiredLabels(t *testing.T, schema json.RawMessage) []string {
	t.Helper()
	var decoded struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Required
}

func makeTaxonomyTestVideo(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	video := filepath.Join(t.TempDir(), "scene.mp4")
	command := exec.Command(ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=black:duration=1:size=32x32:rate=2", "-pix_fmt", "yuv420p", video)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot generate test scene: %v: %s", err, strings.TrimSpace(string(output)))
	}
	return ffmpeg, video
}
