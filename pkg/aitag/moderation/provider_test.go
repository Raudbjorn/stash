package moderation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stashapp/stash/pkg/aitag"
)

// The fake mirrors the endpoint's real shapes, including the two that are easy
// to get wrong: category_scores as graded floats rather than the booleans in
// `categories`, and OpenAI's {"error": {...}} envelope.

type fakeAPI struct {
	t *testing.T

	status int
	scores map[string]float64
	// requests records what the provider sent, so the test can assert on the
	// model name and the input shape rather than only on the reply.
	requests []map[string]any
}

func (f *fakeAPI) start(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			f.t.Errorf("missing bearer token: %q", got)
		}

		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.requests = append(f.requests, body)

		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
			json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]any{"message": "rate limit reached", "type": "rate_limit_error"},
			})
			return
		}

		scores := f.scores
		if scores == nil {
			scores = map[string]float64{"sexual": 0.02, "violence": 0.01}
		}
		json.NewEncoder(w).Encode(response{Results: []struct {
			Flagged        bool               `json:"flagged"`
			CategoryScores map[string]float64 `json:"category_scores"`
			Categories     map[string]bool    `json:"categories"`
		}{{Flagged: false, CategoryScores: scores}}})
	}))
	t.Cleanup(server.Close)
	return server
}

func newProvider(t *testing.T, fake *fakeAPI, ffmpeg string) *Provider {
	t.Helper()
	server := fake.start(t)
	p := New(Config{
		APIKey:        "test-key",
		BaseURL:       server.URL,
		FFmpegPath:    ffmpeg,
		FrameInterval: 2,
		HTTP:          server.Client(),
	})
	t.Cleanup(func() { p.Close() })
	return p
}

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	return path
}

func makeVideo(t *testing.T, ffmpeg string, seconds int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.mp4")
	cmd := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration="+itoa(seconds)+":size=320x240:rate=5",
		"-pix_fmt", "yuv420p", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate video: %v: %s", err, out)
	}
	return path
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestAnalyzeVideoProducesGradedSpans(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	fake := &fakeAPI{t: t, scores: map[string]float64{"sexual": 0.91, "violence": 0.03}}
	p := newProvider(t, fake, ffmpeg)

	video := makeVideo(t, ffmpeg, 8)

	result, err := p.AnalyzeVideo(context.Background(), video, aitag.Options{FrameInterval: 2}, nil)
	if err != nil {
		t.Fatalf("AnalyzeVideo: %v", err)
	}

	spans := result.Spans["moderation"]["sexual"]
	if len(spans) == 0 {
		t.Fatalf("no spans for a category scoring 0.91: %+v", result.Spans)
	}
	// Below threshold, so it must not appear at all.
	if _, ok := result.Spans["moderation"]["violence"]; ok {
		t.Error("a category scoring 0.03 produced spans at the default 0.5 threshold")
	}

	// The graded score is the whole reason this provider is usable; a flag
	// would be worthless downstream.
	if spans[0].Confidence == nil {
		t.Fatal("spans carry no confidence")
	}
	if got := *spans[0].Confidence; got != aitag.QuantizeConfidence(0.91) {
		t.Errorf("confidence = %v, want the quantized 0.91", got)
	}

	// Quantized, so identical frames collapse into ONE span rather than one
	// per frame - the same trap the native provider has.
	if len(spans) != 1 {
		t.Errorf("identical frames produced %d spans, want 1", len(spans))
	}

	// And the request must have named the image-capable model: the text-only
	// ones ignore image input rather than refusing it.
	if len(fake.requests) == 0 {
		t.Fatal("no request was sent")
	}
	if model, _ := fake.requests[0]["model"].(string); model != DefaultModel {
		t.Errorf("model = %q, want %q", model, DefaultModel)
	}
}

// The default sampling must be coarse. This is a rate-limited shared quota, and
// the local pipeline's two-second interval would exhaust it on one scene.
func TestDefaultIntervalIsCoarse(t *testing.T) {
	p := New(Config{APIKey: "k"})

	if got := p.frameInterval(aitag.Options{}); got < 10 {
		t.Errorf("default interval = %vs; a remote rate-limited API needs tens of seconds", got)
	}
	// An explicit request still wins - the user may have a paid quota.
	if got := p.frameInterval(aitag.Options{FrameInterval: 2}); got != 2 {
		t.Errorf("an explicit interval was overridden: %v", got)
	}
}

// A 429 is the failure this provider will actually hit, and "429" alone sends
// the user looking for a bug rather than at their sampling interval.
func TestRateLimitExplainsItself(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	fake := &fakeAPI{t: t, status: http.StatusTooManyRequests}
	p := newProvider(t, fake, ffmpeg)

	_, err := p.AnalyzeVideo(context.Background(), makeVideo(t, ffmpeg, 4), aitag.Options{FrameInterval: 2}, nil)
	if err == nil {
		t.Fatal("a 429 was reported as success")
	}
	if !strings.Contains(err.Error(), "frame interval") {
		t.Errorf("the error does not point at the fix: %v", err)
	}
}

func TestUnconfiguredProviderSaysSo(t *testing.T) {
	p := New(Config{})

	if err := p.Available(context.Background()); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("Available = %v, want ErrNoAPIKey", err)
	}
	if _, err := p.AnalyzeVideo(context.Background(), "/x.mp4", aitag.Options{}, nil); !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("AnalyzeVideo = %v, want ErrNoAPIKey", err)
	}
}

// One unreadable image must not lose the batch.
func TestAnalyzeImagesRecordsPerImageErrors(t *testing.T) {
	fake := &fakeAPI{t: t, scores: map[string]float64{"sexual": 0.8}}
	p := newProvider(t, fake, "")

	good := filepath.Join(t.TempDir(), "a.png")
	png, err := aitag.EncodePNG(make([]byte, 8*8*3), 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, png, 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := p.AnalyzeImages(context.Background(), []string{good, "/nonexistent.png"}, aitag.Options{})
	if err != nil {
		t.Fatalf("AnalyzeImages: %v", err)
	}
	if len(result.Tags) != 1 {
		t.Errorf("tagged %d images, want 1", len(result.Tags))
	}
	if result.Errors["/nonexistent.png"] == "" {
		t.Error("the unreadable image has no recorded reason")
	}
	if labels := result.Tags[good]["moderation"]; len(labels) != 1 || labels[0] != "sexual" {
		t.Errorf("labels = %v", labels)
	}
}

// The provider must be honest that it is a coarse gate: claiming to be a tagger
// would have a user reach for it instead of the local pipeline.
func TestCapabilitiesAreHonest(t *testing.T) {
	p := New(Config{APIKey: "k"})

	caps := p.Capabilities()
	if !caps.Has(aitag.CapConfidence) {
		t.Error("the provider does not claim graded confidences, which is its only real advantage")
	}
	if caps.Has(aitag.CapEmbeddings) {
		t.Error("the provider claims embeddings it cannot produce")
	}
}

func TestEncodePNGRoundTrips(t *testing.T) {
	rgb := []byte{255, 0, 0, 0, 255, 0, 0, 0, 255, 255, 255, 255}
	data, err := aitag.EncodePNG(rgb, 2, 2)
	if err != nil {
		t.Fatalf("encodePNG: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("no PNG produced")
	}
	if _, err := aitag.EncodePNG(rgb, 4, 4); err == nil {
		t.Error("a frame of the wrong size was accepted")
	}
}
