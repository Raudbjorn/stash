package recommend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/store"
)

func TestVoyageSegmentIndexBuildsThreeFixedSegments(t *testing.T) {
	index, calls, videoPath := newTestVoyageSegmentIndex(t)
	segments, err := index.Build(context.Background(), 42, videoPath, "transcript", 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 || calls.Load() != 3 {
		t.Fatalf("segments=%d HTTP calls=%d, want 3/3", len(segments), calls.Load())
	}
	for i, segment := range segments {
		if segment.Start != float64(i*30) || segment.End != float64((i+1)*30) || len(segment.Vector) != defaultVoyageDimension {
			t.Fatalf("segment %d = %#v", i, segment)
		}
	}
}

func TestVoyageSegmentIndexCacheUsesSourceFingerprint(t *testing.T) {
	index, calls, videoPath := newTestVoyageSegmentIndex(t)
	if _, err := index.Build(context.Background(), 42, videoPath, "transcript", 90); err != nil {
		t.Fatal(err)
	}
	firstCalls := calls.Load()
	extractVideo := index.ExtractVideo
	index.ExtractVideo = func(context.Context, string, float64, float64) ([]byte, error) {
		return nil, errors.New("cached build extracted video")
	}
	if _, err := index.Build(context.Background(), 42, videoPath, "transcript", 90); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != firstCalls {
		t.Fatalf("unchanged input made %d calls, want %d", calls.Load(), firstCalls)
	}

	index.ExtractVideo = extractVideo
	if _, err := index.Build(context.Background(), 42, videoPath, "different transcript", 90); err != nil {
		t.Fatal(err)
	}
	afterTranscript := calls.Load()
	if afterTranscript != firstCalls+3 {
		t.Fatalf("changed transcript made %d calls, want %d", afterTranscript, firstCalls+3)
	}

	if err := os.WriteFile(videoPath, []byte("changed source metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	index.ExtractVideo = func(context.Context, string, float64, float64) ([]byte, error) {
		return []byte("different mp4"), nil
	}
	if _, err := index.Build(context.Background(), 42, videoPath, "different transcript", 90); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != afterTranscript+3 {
		t.Fatalf("changed video made %d calls, want %d", calls.Load(), afterTranscript+3)
	}
}

func newTestVoyageSegmentIndex(t *testing.T) (*VoyageSegmentIndex, *atomic.Int32, string) {
	t.Helper()
	tempDir := t.TempDir()
	db, err := store.Open(context.Background(), filepath.Join(tempDir, "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	videoPath := filepath.Join(tempDir, "scene.mp4")
	if err := os.WriteFile(videoPath, []byte("source metadata"), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Inputs []struct {
				Content []map[string]any `json:"content"`
			} `json:"inputs"`
			Model           string `json:"model"`
			InputType       string `json:"input_type"`
			OutputDimension int    `json:"output_dimension"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Inputs) != 1 || len(request.Inputs[0].Content) != 2 ||
			request.Inputs[0].Content[1]["type"] != "video_base64" ||
			request.Model != "voyage-multimodal-3.5" || request.InputType != "document" ||
			request.OutputDimension != defaultVoyageDimension {
			t.Errorf("request = %#v", request)
		}
		vector := make([]float32, defaultVoyageDimension)
		vector[0] = 1
		vector[len(vector)-1] = 3
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": vector}},
		})
	}))
	t.Cleanup(server.Close)

	index := &VoyageSegmentIndex{
		APIKey: "secret", Model: "voyage-multimodal-3.5", Endpoint: server.URL,
		SegmentSecs: 30, DB: db, Client: server.Client(), FFmpegPath: "ffmpeg-not-installed-for-test",
		ExtractVideo: func(context.Context, string, float64, float64) ([]byte, error) {
			return []byte("mp4"), nil
		},
	}
	return index, &calls, videoPath
}
