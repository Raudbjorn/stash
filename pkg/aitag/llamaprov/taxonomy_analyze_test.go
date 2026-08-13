package llamaprov

import (
	"context"
	"encoding/json"
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
