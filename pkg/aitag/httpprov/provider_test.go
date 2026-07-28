package httpprov

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
)

// The fake server mirrors the real v3 API's shapes exactly, including the parts
// that are easy to get wrong: the result nested under "result", spans with a
// null end and no confidence, and FastAPI's {"detail": ...} error envelope.

type fakeServer struct {
	t *testing.T

	ready      int
	videoBody  videoRequest
	videoDelay time.Duration
	failVideo  any
}

func (f *fakeServer) start(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		status := f.ready
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	})

	mux.HandleFunc("/v3/current_ai_models/", func(w http.ResponseWriter, r *http.Request) {
		id := 7
		version := 1.5
		json.NewEncoder(w).Encode([]modelInfo{{
			Name: "vivid_galaxy", Identifier: &id, Version: &version,
			Categories: []string{"actions"}, Type: "video",
		}})
	})

	mux.HandleFunc("/v3/process_video/", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&f.videoBody); err != nil {
			f.t.Errorf("decoding the request the provider sent: %v", err)
		}
		if f.videoDelay > 0 {
			select {
			case <-time.After(f.videoDelay):
			case <-r.Context().Done():
				return
			}
		}
		if f.failVideo != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"detail": f.failVideo})
			return
		}

		end := 12.0
		w.Write([]byte(`{
			"result": {
				"schema_version": 3,
				"duration": 600.5,
				"frame_interval": 2,
				"models": [{"name":"vivid_galaxy","identifier":7,"version":1.5,"categories":["actions"],"type":"video"}],
				"timespans": {
					"actions": {
						"Blowjob": [{"start": 4, "end": ` + jsonFloat(end) + `}, {"start": 40, "end": null}],
						"Kissing": [{"start": 0, "end": 2, "confidence": 0.87}]
					}
				}
			},
			"metrics": {"ai_inference_seconds": 12.5}
		}`))
	})

	mux.HandleFunc("/v3/process_images/", func(w http.ResponseWriter, r *http.Request) {
		var req imageRequest
		json.NewDecoder(r.Body).Decode(&req)

		results := make([]map[string]any, 0, len(req.Paths))
		for i := range req.Paths {
			if i == 1 {
				// One bad image in the batch, which must not lose the others.
				results = append(results, map[string]any{"error": "cannot read file"})
				continue
			}
			results = append(results, map[string]any{
				"actions":   []any{"Blowjob", []any{"Kissing", 0.9}},
				"bodyparts": []any{"Feet"},
			})
		}
		json.NewEncoder(w).Encode(map[string]any{
			"result": results,
			"models": []modelInfo{{Name: "image_model", Categories: []string{"actions"}}},
		})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func jsonFloat(v float64) string {
	encoded, _ := json.Marshal(v)
	return string(encoded)
}

func newProvider(t *testing.T, url string) *Provider {
	t.Helper()
	p := New(Config{ServerURL: url, HTTP: &http.Client{Timeout: 10 * time.Second}})
	t.Cleanup(func() { p.Close() })
	return p
}

func TestAnalyzeVideoDecodesTheV3Shape(t *testing.T) {
	fake := &fakeServer{t: t}
	server := fake.start(t)
	p := newProvider(t, server.URL)

	var progress []aitag.Progress
	result, err := p.AnalyzeVideo(context.Background(), "/media/scene.mp4",
		aitag.Options{FrameInterval: 4, Threshold: 0.6, VR: true, SkipCategories: []string{"bodyparts"}},
		func(p aitag.Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatalf("AnalyzeVideo: %v", err)
	}

	// The request must carry what the caller asked for, or the server silently
	// uses its own defaults and the user's settings do nothing.
	if fake.videoBody.Path != "/media/scene.mp4" {
		t.Errorf("path = %q", fake.videoBody.Path)
	}
	if fake.videoBody.FrameInterval != 4 || fake.videoBody.Threshold != 0.6 {
		t.Errorf("frame interval/threshold = %v/%v, want 4/0.6",
			fake.videoBody.FrameInterval, fake.videoBody.Threshold)
	}
	if !fake.videoBody.VRVideo {
		t.Error("the VR flag did not reach the server")
	}
	if len(fake.videoBody.CategoriesToSkip) != 1 {
		t.Errorf("categories_to_skip = %v", fake.videoBody.CategoriesToSkip)
	}

	if result.Duration != 600.5 || result.FrameInterval != 2 {
		t.Errorf("duration/interval = %v/%v", result.Duration, result.FrameInterval)
	}
	if len(result.Models) != 1 || result.Models[0].Name != "vivid_galaxy" {
		t.Errorf("models = %+v", result.Models)
	}

	spans := result.Spans["actions"]["Blowjob"]
	if len(spans) != 2 {
		t.Fatalf("Blowjob spans = %+v", spans)
	}
	// A closed span and an open one must round-trip as such: the difference is
	// load-bearing everywhere downstream.
	if spans[0].End == nil || *spans[0].End != 12 {
		t.Errorf("first span end = %v, want 12", spans[0].End)
	}
	if spans[1].End != nil {
		t.Errorf("second span should be open, got end %v", *spans[1].End)
	}
	if spans[0].Confidence != nil {
		t.Errorf("v3 returns no confidences, got %v", *spans[0].Confidence)
	}

	if conf := result.Spans["actions"]["Kissing"][0].Confidence; conf == nil || *conf != 0.87 {
		t.Errorf("a supplied confidence was lost: %v", conf)
	}

	if result.Metrics["ai_inference_seconds"] != 12.5 {
		t.Errorf("metrics = %v", result.Metrics)
	}

	// Exactly two reports: one saying no incremental progress is available, one
	// on completion. A fake progress bar would be worse than none.
	if len(progress) != 2 {
		t.Fatalf("progress reports = %d, want 2: %+v", len(progress), progress)
	}
	if progress[0].Fraction >= 0 {
		t.Errorf("the first report claims a fraction the server cannot supply: %v", progress[0].Fraction)
	}
	if progress[1].Fraction != 1 {
		t.Errorf("final fraction = %v, want 1", progress[1].Fraction)
	}
}

// Capabilities must not claim confidences: the v3 route hard-codes
// return_confidence=false, and claiming otherwise would make the span-merge
// rule look broken downstream.
func TestCapabilitiesDoNotClaimConfidence(t *testing.T) {
	p := New(Config{ServerURL: "https://example.invalid"})
	if p.Capabilities().Has(aitag.CapConfidence) {
		t.Error("the provider claims confidences the v3 API does not return")
	}
	if !p.Capabilities().Has(aitag.CapVideo | aitag.CapImages) {
		t.Error("the provider does not claim video and images")
	}
}

// FastAPI puts the real reason in {"detail": ...}; a bare status code throws
// away the only useful part of a failure.
func TestServerErrorsCarryTheDetail(t *testing.T) {
	fake := &fakeServer{t: t, failVideo: "ffmpeg could not open the file"}
	server := fake.start(t)
	p := newProvider(t, server.URL)

	_, err := p.AnalyzeVideo(context.Background(), "/media/broken.mp4", aitag.Options{}, nil)
	if err == nil {
		t.Fatal("a 400 was reported as success")
	}
	if !strings.Contains(err.Error(), "ffmpeg could not open the file") {
		t.Errorf("the detail did not survive: %v", err)
	}
}

// A structured detail - the shape used for machine-readable codes - must also
// survive rather than rendering as "map[...]".
func TestStructuredErrorDetailSurvives(t *testing.T) {
	fake := &fakeServer{t: t, failVideo: map[string]any{"code": "MODEL_MISSING"}}
	server := fake.start(t)
	p := newProvider(t, server.URL)

	_, err := p.AnalyzeVideo(context.Background(), "/media/x.mp4", aitag.Options{}, nil)
	if err == nil || !strings.Contains(err.Error(), "MODEL_MISSING") {
		t.Errorf("err = %v, want it to name the code", err)
	}
}

// A user cancelling a long analysis must not be reported as a server failure.
func TestCancellationIsNotAServerFailure(t *testing.T) {
	fake := &fakeServer{t: t, videoDelay: 5 * time.Second}
	server := fake.start(t)
	p := newProvider(t, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(150 * time.Millisecond)
		cancel()
	}()

	_, err := p.AnalyzeVideo(ctx, "/media/long.mp4", aitag.Options{}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestAvailableProbesReadiness(t *testing.T) {
	fake := &fakeServer{t: t}
	server := fake.start(t)
	p := newProvider(t, server.URL)

	if err := p.Available(context.Background()); err != nil {
		t.Errorf("Available: %v", err)
	}

	// Anything below 400 counts as ready: a server that answers at all is up.
	fake.ready = http.StatusNoContent
	if err := p.Available(context.Background()); err != nil {
		t.Errorf("204 was treated as not ready: %v", err)
	}

	fake.ready = http.StatusServiceUnavailable
	if err := p.Available(context.Background()); err == nil {
		t.Error("503 was treated as ready")
	}
}

// With no server configured the provider must say so plainly rather than
// attempting a request against an empty URL.
func TestUnconfiguredProviderSaysSo(t *testing.T) {
	p := New(Config{})

	if err := p.Available(context.Background()); !errors.Is(err, ErrNoServer) {
		t.Errorf("Available = %v, want ErrNoServer", err)
	}
	if _, err := p.AnalyzeVideo(context.Background(), "/x", aitag.Options{}, nil); !errors.Is(err, ErrNoServer) {
		t.Errorf("AnalyzeVideo = %v, want ErrNoServer", err)
	}
}

// One unreadable image must not lose the rest of the batch.
func TestAnalyzeImagesRecordsPerImageErrors(t *testing.T) {
	fake := &fakeServer{t: t}
	server := fake.start(t)
	p := newProvider(t, server.URL)

	paths := []string{"/a.jpg", "/b.jpg", "/c.jpg"}
	result, err := p.AnalyzeImages(context.Background(), paths, aitag.Options{})
	if err != nil {
		t.Fatalf("AnalyzeImages: %v", err)
	}

	if len(result.Tags) != 2 {
		t.Errorf("tagged %d images, want 2: %v", len(result.Tags), result.Tags)
	}
	if result.Errors["/b.jpg"] == "" {
		t.Error("the failing image has no recorded reason")
	}

	// Both the plain and (name, confidence) label shapes must decode to a name.
	actions := result.Tags["/a.jpg"]["actions"]
	if len(actions) != 2 {
		t.Fatalf("actions = %v, want two labels", actions)
	}
	if actions[0] != "Blowjob" || actions[1] != "Kissing" {
		t.Errorf("labels = %v", actions)
	}
}

func TestAnalyzeImagesWithNoPathsDoesNotCallTheServer(t *testing.T) {
	p := New(Config{ServerURL: "http://127.0.0.1:1"})

	result, err := p.AnalyzeImages(context.Background(), nil, aitag.Options{})
	if err != nil {
		t.Fatalf("AnalyzeImages: %v", err)
	}
	if len(result.Tags) != 0 {
		t.Errorf("tags = %v, want empty", result.Tags)
	}
}

func TestModelsAreListed(t *testing.T) {
	fake := &fakeServer{t: t}
	server := fake.start(t)
	p := newProvider(t, server.URL)

	models, err := p.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if len(models) != 1 || models[0].Name != "vivid_galaxy" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].Identifier == nil || *models[0].Identifier != 7 {
		t.Errorf("identifier = %v", models[0].Identifier)
	}
}

// Spans must come back in time order whatever the server's map iteration
// produced: clustering walks them in order and would otherwise emit
// overlapping markers rather than an error.
func TestSpansAreSorted(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v3/process_video/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"result":{"schema_version":3,"duration":100,"frame_interval":2,
			"timespans":{"actions":{"Blowjob":[{"start":40},{"start":4},{"start":20}]}}}}`))
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	p := newProvider(t, server.URL)
	result, err := p.AnalyzeVideo(context.Background(), "/x.mp4", aitag.Options{}, nil)
	if err != nil {
		t.Fatal(err)
	}

	spans := result.Spans["actions"]["Blowjob"]
	for i := 1; i < len(spans); i++ {
		if spans[i-1].Start > spans[i].Start {
			t.Fatalf("spans are not sorted: %+v", spans)
		}
	}
}
