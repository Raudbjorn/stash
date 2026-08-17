package recommend

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stashapp/stash/internal/aiserver/store"
)

func TestVoyageSegmentIndexBuildsThreeFixedSegments(t *testing.T) {
	index, calls := newTestVoyageSegmentIndex(t)
	segments, err := index.Build(context.Background(), 42, "missing.mp4", "transcript", 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 || calls.Load() != 3 {
		t.Fatalf("segments=%d HTTP calls=%d, want 3/3", len(segments), calls.Load())
	}
	for i, segment := range segments {
		if segment.Start != float64(i*30) || segment.End != float64((i+1)*30) || len(segment.Vector) != 3 {
			t.Fatalf("segment %d = %#v", i, segment)
		}
	}
}

func TestVoyageSegmentIndexUsesDatabaseCache(t *testing.T) {
	index, calls := newTestVoyageSegmentIndex(t)
	if _, err := index.Build(context.Background(), 42, "missing.mp4", "transcript", 90); err != nil {
		t.Fatal(err)
	}
	firstCalls := calls.Load()
	segments, err := index.Build(context.Background(), 42, "missing.mp4", "different transcript", 90)
	if err != nil {
		t.Fatal(err)
	}
	if len(segments) != 3 || calls.Load() != firstCalls {
		t.Fatalf("cache build segments=%d calls=%d, want 3/%d", len(segments), calls.Load(), firstCalls)
	}
}

func newTestVoyageSegmentIndex(t *testing.T) (*VoyageSegmentIndex, *atomic.Int32) {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var request struct {
			Inputs []struct {
				Content []map[string]any `json:"content"`
			} `json:"inputs"`
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Inputs) != 1 || len(request.Inputs[0].Content) != 6 || request.Model != "voyage-multimodal-3.5" {
			t.Errorf("request = %#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"index": 0, "embedding": []float32{1, 2, 3}}},
		})
	}))
	t.Cleanup(server.Close)

	frame := testJPEG(t)
	return &VoyageSegmentIndex{
		APIKey: "secret", Model: "voyage-multimodal-3.5", Endpoint: server.URL,
		SegmentSecs: 30, Dimension: 3, DB: db, Client: server.Client(), FFmpegPath: "ffmpeg-not-installed-for-test",
		ExtractFrame: func(context.Context, string, float64) ([]byte, error) {
			return frame, nil
		},
	}, &calls
}

func testJPEG(t *testing.T) []byte {
	t.Helper()
	imageValue := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := range 4 {
		for x := range 4 {
			imageValue.Set(x, y, color.RGBA{R: 32, G: 64, B: 96, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, imageValue, nil); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}
