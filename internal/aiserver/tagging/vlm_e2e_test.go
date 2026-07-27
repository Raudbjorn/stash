package tagging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
)

func TestVLMGeneratedVideoFlowsThroughClusteringAndWriteback(t *testing.T) {
	ffmpeg, video := makeVLMTestVideo(t)

	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		frame := requests.Add(1)
		decisions, _ := json.Marshal(map[string]string{
			"action": map[bool]string{true: "yes", false: "no"}[frame <= 2],
			"other":  "no",
		})
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []any{map[string]any{"message": map[string]any{"content": string(decisions)}}},
		})
	}))
	defer server.Close()

	provider, err := llamaprov.New(llamaprov.Config{
		Client: server.Client(), BaseURL: server.URL,
		Pair:   assets.Pair{Name: "deterministic-vlm", ContextTokens: 4096},
		Labels: []string{"action", "other"}, FFmpegPath: ffmpeg,
		DefaultInterval: 2, MaxMergeSeconds: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	analyze := func() *aitag.Result {
		t.Helper()
		requests.Store(0)
		result, err := provider.AnalyzeVideo(context.Background(), video, aitag.Options{FrameInterval: 2}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if requests.Load() != 3 {
			t.Fatalf("requests=%d, want one request for each of three frames", requests.Load())
		}
		if got := aitag.CountSpans(result.Spans); got != 1 {
			t.Fatalf("raw collapsed spans=%d: %+v", got, result.Spans)
		}
		return result
	}

	rules := aitag.NewRules()
	rules.Categories["actions"] = aitag.CategoryRules{
		"action": {
			OriginalTag: "action", RenamedTag: "Action_AI",
			MinMarkerDuration: "1", TagThreshold: "0",
		},
	}
	firstResult := analyze()
	firstMarkers := aitag.Cluster(firstResult, rules, aitag.DefaultClusterParams())
	if len(firstMarkers) != 1 || firstMarkers[0].Start != 0 || firstMarkers[0].RenamedTag != "Action_AI" {
		t.Fatalf("clustered markers = %+v", firstMarkers)
	}

	writer, _, tags, _ := newTestWriter(t)
	tags.byName["Action_AI"] = 5
	first, err := writer.Write(context.Background(), llamaprov.ProviderName, 42, 1,
		firstMarkers, firstResult.FrameInterval, DefaultWritebackOptions())
	if err != nil {
		t.Fatal(err)
	}
	if first.Created != 1 || first.Removed != 0 {
		t.Fatalf("first writeback = %+v", first)
	}

	secondResult := analyze()
	secondMarkers := aitag.Cluster(secondResult, rules, aitag.DefaultClusterParams())
	second, err := writer.Write(context.Background(), llamaprov.ProviderName, 42, 2,
		secondMarkers, secondResult.FrameInterval, DefaultWritebackOptions())
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != first.Created || second.Removed != first.Created {
		t.Fatalf("idempotent replacement first=%+v second=%+v", first, second)
	}
}

func makeVLMTestVideo(t *testing.T) (string, string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	video := filepath.Join(t.TempDir(), "scene.mp4")
	command := exec.Command(ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=6:size=64x64:rate=10", "-pix_fmt", "yuv420p", video)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot generate the test scene: %v: %s", err, output)
	}
	return ffmpeg, video
}
