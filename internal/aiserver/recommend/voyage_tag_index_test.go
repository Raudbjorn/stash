package recommend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/taxonomy"
)

func TestVoyageTagIndexMatchesAndCachesSceneAndTaxonomyVectors(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	cache := &taxonomy.Cache{
		UpdatedAt: time.Now(),
		Entries: map[string]taxonomy.Entry{
			"alpha": {StashID: "alpha", Canonical: "Alpha act", Category: "Acts"},
			"beta":  {StashID: "beta", Canonical: "Beta act", Category: "Acts"},
			"low":   {StashID: "low", Canonical: "Low match", Category: "Acts"},
		},
	}
	client := &taxonomy.Client{Cache: cache}

	var documentCalls atomic.Int32
	var queryCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Inputs []struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"inputs"`
			InputType       string `json:"input_type"`
			OutputDimension int    `json:"output_dimension"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.OutputDimension != 2 {
			t.Errorf("output_dimension=%d, want 2", request.OutputDimension)
		}
		data := make([]map[string]any, len(request.Inputs))
		switch request.InputType {
		case "document":
			call := documentCalls.Add(1)
			vector := []float32{1, 0}
			if call == 2 {
				vector = []float32{0, 1}
			}
			data[0] = map[string]any{"index": 0, "embedding": vector}
		case "query":
			queryCalls.Add(1)
			for i, input := range request.Inputs {
				text := input.Content[0].Text
				vector := []float32{-1, 0}
				switch {
				case strings.Contains(text, "Alpha act"):
					vector = []float32{1, 0}
				case strings.Contains(text, "Beta act"):
					vector = []float32{0, 1}
				}
				data[i] = map[string]any{"index": i, "embedding": vector}
			}
		default:
			t.Fatalf("input_type=%q", request.InputType)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(server.Close)

	index := &VoyageSegmentIndex{
		APIKey: "secret", Model: "voyage-multimodal-3.5", Endpoint: server.URL,
		SegmentSecs: 30, Dimension: 2, DB: db, Client: server.Client(),
		FFmpegPath: "ffmpeg-not-installed-for-test", Taxonomy: client, Categories: []string{"Acts"},
		ExtractVideo: func(context.Context, string, float64, float64) ([]byte, error) {
			return []byte("mp4"), nil
		},
	}

	result, err := index.AnalyzeVideoTags(context.Background(), 42, "missing.mp4", 60, 0.9, aitag.Sink(func(aitag.Progress) {}))
	if err != nil {
		t.Fatal(err)
	}
	acts := result.Spans["Acts"]
	if len(acts) != 2 || len(acts["Alpha act"]) != 1 || len(acts["Beta act"]) != 1 {
		t.Fatalf("spans=%#v", result.Spans)
	}
	if acts["Alpha act"][0].Start != 0 || acts["Beta act"][0].Start != 30 {
		t.Fatalf("Alpha=%#v Beta=%#v", acts["Alpha act"], acts["Beta act"])
	}
	if _, ok := acts["Low match"]; ok {
		t.Fatalf("low-similarity tag was accepted: %#v", acts["Low match"])
	}
	if documentCalls.Load() != 2 || queryCalls.Load() != 1 {
		t.Fatalf("document calls=%d query calls=%d, want 2/1", documentCalls.Load(), queryCalls.Load())
	}

	if _, err := index.AnalyzeVideoTags(context.Background(), 42, "missing.mp4", 60, 0.9, aitag.Sink(func(aitag.Progress) {})); err != nil {
		t.Fatal(err)
	}
	if documentCalls.Load() != 2 || queryCalls.Load() != 1 {
		t.Fatalf("cached document calls=%d query calls=%d, want 2/1", documentCalls.Load(), queryCalls.Load())
	}
}

func TestVoyageTagIndexRejectsInvalidThreshold(t *testing.T) {
	index := &VoyageSegmentIndex{APIKey: "secret", DB: &store.DB{}, Taxonomy: &taxonomy.Client{}}
	_, err := index.AnalyzeVideoTags(context.Background(), 1, "scene.mp4", 30, 1.01, aitag.Sink(func(aitag.Progress) {}))
	if err == nil || !strings.Contains(err.Error(), "exceeds 1") {
		t.Fatalf("error=%v", err)
	}
}
