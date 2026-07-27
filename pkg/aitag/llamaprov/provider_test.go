package llamaprov

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
)

func newTestProvider(t *testing.T, server *httptest.Server, labels ...string) *Provider {
	t.Helper()
	provider, err := New(Config{
		Client: server.Client(), BaseURL: server.URL,
		Pair:   assets.Pair{Name: "vision-pair", Description: "test vision pair"},
		Labels: labels, FFmpegPath: "ffmpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}

func writeCompletion(t *testing.T, w http.ResponseWriter, decisions map[string]string) {
	t.Helper()
	content, err := json.Marshal(decisions)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{"content": string(content)},
		}},
	})
}

func TestNewNormalizesAndValidatesLabels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	provider := newTestProvider(t, server, "  zeta ", "Alpha", "beta")
	if got, want := provider.Labels(), []string{"Alpha", "beta", "zeta"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Labels = %q, want %q", got, want)
	}
	got := provider.Labels()
	got[0] = "mutated"
	if provider.Labels()[0] != "Alpha" {
		t.Fatal("Labels exposed mutable provider state")
	}

	base := Config{Client: server.Client(), BaseURL: server.URL, Pair: assets.Pair{Name: "pair"}, FFmpegPath: "ffmpeg"}
	for name, labels := range map[string][]string{
		"missing":         nil,
		"empty":           {"valid", "  "},
		"exact duplicate": {"same", "same"},
		"case duplicate":  {"Same", "same"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			cfg.Labels = labels
			if _, err := New(cfg); err == nil {
				t.Fatal("invalid labels were accepted")
			}
		})
	}
}

func TestClassifyFrameSendsStrictSingleRequest(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		var request completionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Model != "vision-pair" || request.Temperature != 0 || request.Stream || !request.CachePrompt {
			t.Errorf("sampling contract not deterministic: %+v", request)
		}
		if request.MaxTokens < 64 || request.MaxTokens > 4096 {
			t.Errorf("max_tokens = %d", request.MaxTokens)
		}
		if len(request.Messages) != 1 || request.Messages[0].Role != "user" || len(request.Messages[0].Content) != 2 {
			t.Fatalf("messages = %+v", request.Messages)
		}
		parts := request.Messages[0].Content
		if parts[0].Type != "text" || !strings.HasPrefix(parts[0].Text, classificationInstruction) || !strings.Contains(parts[0].Text, `["Alpha","beta"]`) {
			t.Errorf("prompt = %q", parts[0].Text)
		}
		if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
			t.Fatalf("image part = %+v", parts[1])
		}
		const prefix = "data:image/png;base64,"
		if !strings.HasPrefix(parts[1].ImageURL.URL, prefix) {
			t.Fatalf("image URL is not an inline PNG")
		}
		encoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(parts[1].ImageURL.URL, prefix))
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := png.Decode(strings.NewReader(string(encoded)))
		if err != nil {
			t.Fatalf("decode request PNG: %v", err)
		}
		if decoded.Bounds().Dx() != 2 || decoded.Bounds().Dy() != 1 {
			t.Errorf("image dimensions = %v", decoded.Bounds())
		}
		if request.ResponseFormat.Type != "json_schema" || !request.ResponseFormat.JSONSchema.Strict {
			t.Errorf("response format = %+v", request.ResponseFormat)
		}
		var schema struct {
			Type                 string                     `json:"type"`
			Properties           map[string]json.RawMessage `json:"properties"`
			Required             []string                   `json:"required"`
			AdditionalProperties bool                       `json:"additionalProperties"`
		}
		if err := json.Unmarshal(request.ResponseFormat.JSONSchema.Schema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema.Type != "object" || schema.AdditionalProperties || !reflect.DeepEqual(schema.Required, []string{"Alpha", "beta"}) || len(schema.Properties) != 2 {
			t.Errorf("schema = %+v", schema)
		}
		for label, property := range schema.Properties {
			var definition struct {
				Type string   `json:"type"`
				Enum []string `json:"enum"`
			}
			if err := json.Unmarshal(property, &definition); err != nil {
				t.Fatal(err)
			}
			if definition.Type != "string" || !reflect.DeepEqual(definition.Enum, []string{"yes", "no"}) {
				t.Errorf("property %q = %+v", label, definition)
			}
		}
		writeCompletion(t, w, map[string]string{"Alpha": "yes", "beta": "no"})
	}))
	defer server.Close()
	provider := newTestProvider(t, server, "beta", "Alpha")
	decisions, err := provider.ClassifyFrame(context.Background(), []byte{255, 0, 0, 0, 255, 0}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !decisions["Alpha"] || decisions["beta"] {
		t.Fatalf("calls=%d decisions=%v", calls, decisions)
	}
}

func TestClassifyFrameRejectsMalformedResponses(t *testing.T) {
	tests := map[string]string{
		"zero choices":         `{"choices":[]}`,
		"two choices":          `{"choices":[{"message":{"content":"{}"}},{"message":{"content":"{}"}}]}`,
		"non-text content":     `{"choices":[{"message":{"content":{"a":"yes"}}}]}`,
		"invalid content JSON": `{"choices":[{"message":{"content":"not json"}}]}`,
		"missing label":        `{"choices":[{"message":{"content":"{\"a\":\"yes\"}"}}]}`,
		"extra label":          `{"choices":[{"message":{"content":"{\"a\":\"yes\",\"b\":\"no\",\"extra\":\"yes\"}"}}]}`,
		"unknown decision":     `{"choices":[{"message":{"content":"{\"a\":\"maybe\",\"b\":\"no\"}"}}]}`,
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			provider := newTestProvider(t, server, "a", "b")
			if _, err := provider.ClassifyFrame(context.Background(), make([]byte, 3), 1, 1); err == nil {
				t.Fatal("malformed response was accepted")
			}
		})
	}
}

func TestClassifyFrameSurfacesServerEnvelopeAndCancellation(t *testing.T) {
	t.Run("error envelope", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"bad_schema","message":"schema rejected","type":"invalid_request"}}`))
		}))
		defer server.Close()
		provider := newTestProvider(t, server, "a")
		_, err := provider.ClassifyFrame(context.Background(), make([]byte, 3), 1, 1)
		if err == nil || !strings.Contains(err.Error(), "bad_schema") || !strings.Contains(err.Error(), "schema rejected") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			<-release
		}))
		defer server.Close()
		provider := newTestProvider(t, server, "a")
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-started; cancel() }()
		_, err := provider.ClassifyFrame(ctx, make([]byte, 3), 1, 1)
		close(release)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	})
}

type closeTrackingTransport struct{ closes atomic.Int32 }

func (*closeTrackingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("unexpected request")
}
func (t *closeTrackingTransport) CloseIdleConnections() { t.closes.Add(1) }

func TestProviderMetadataAvailabilityAndIdempotentClose(t *testing.T) {
	transport := &closeTrackingTransport{}
	var available, waitReady, closeHost atomic.Int32
	provider, err := New(Config{
		Client: &http.Client{Transport: transport}, Pair: assets.Pair{Name: "pair"},
		Labels: []string{"tag"}, FFmpegPath: "ffmpeg",
		Available: func() error { available.Add(1); return nil },
		WaitReady: func(context.Context) error { waitReady.Add(1); return nil },
		CloseHost: func() { closeHost.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != ProviderName || provider.Capabilities() != aitag.CapVideo|aitag.CapImages {
		t.Errorf("identity = %s/%s", provider.Name(), provider.Capabilities())
	}
	if err := provider.Available(context.Background()); err != nil || available.Load() != 1 || waitReady.Load() != 0 {
		t.Errorf("Available waited for startup: available=%d wait=%d err=%v", available.Load(), waitReady.Load(), err)
	}
	models, err := provider.Models(context.Background())
	if err != nil || len(models) != 1 || models[0].Name != "pair" || models[0].Type != "vision-language" || models[0].FrameInterval != DefaultFrameInterval {
		t.Fatalf("Models = %+v, err=%v", models, err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	if closeHost.Load() != 1 || transport.closes.Load() != 1 {
		t.Errorf("close counts host=%d transport=%d", closeHost.Load(), transport.closes.Load())
	}
}

func TestAnalyzeImagesKeepsPerPathErrors(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		writeCompletion(t, w, map[string]string{"a": "yes", "b": "no"})
	}))
	defer server.Close()
	provider := newTestProvider(t, server, "b", "a")
	dir := t.TempDir()
	good := filepath.Join(dir, "good.png")
	file, err := os.Create(good)
	if err != nil {
		t.Fatal(err)
	}
	goodImage := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	goodImage.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	if err := png.Encode(file, goodImage); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.png")
	if err := os.WriteFile(bad, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing.png")

	result, err := provider.AnalyzeImages(context.Background(), []string{bad, good, missing}, aitag.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want one for the readable image", requests.Load())
	}
	if got := result.Tags[good][defaultCategory]; !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("tags = %q", got)
	}
	if result.Errors[bad] == "" || result.Errors[missing] == "" || len(result.Errors) != 2 {
		t.Errorf("errors = %v", result.Errors)
	}
}

func TestAnalyzeVideoStreamsFramesSequentially(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg is not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe is not installed")
	}
	video := filepath.Join(t.TempDir(), "video.mp4")
	cmd := exec.Command(ffmpeg, "-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=3:size=32x32:rate=10", "-pix_fmt", "yuv420p", video)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg cannot generate a fixture: %v: %s", err, output)
	}
	var requests, active, maxActive atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		current := active.Add(1)
		for current > maxActive.Load() && !maxActive.CompareAndSwap(maxActive.Load(), current) {
		}
		time.Sleep(5 * time.Millisecond)
		active.Add(-1)
		writeCompletion(t, w, map[string]string{"action": "yes", "other": "no"})
	}))
	defer server.Close()
	provider, err := New(Config{
		Client: server.Client(), BaseURL: server.URL,
		Pair: assets.Pair{Name: "vision-pair"}, Labels: []string{"other", "action"},
		FFmpegPath: ffmpeg, DefaultInterval: 30, MaxMergeSeconds: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	var progress []aitag.Progress
	result, err := provider.AnalyzeVideo(context.Background(), video, aitag.Options{FrameInterval: 1}, func(update aitag.Progress) {
		progress = append(progress, update)
	})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 3 || maxActive.Load() != 1 {
		t.Fatalf("requests=%d max concurrent=%d, want 3/1", requests.Load(), maxActive.Load())
	}
	if result.SchemaVersion != 3 || result.FrameInterval != 1 || result.Duration < 2.9 || result.Duration > 3.1 {
		t.Errorf("result metadata = %+v", result)
	}
	spans := result.Spans[defaultCategory]["action"]
	if len(spans) != 1 || spans[0].Start != 0 || spans[0].End == nil || *spans[0].End != 2 {
		t.Errorf("action spans = %+v", spans)
	}
	if len(result.Spans[defaultCategory]["other"]) != 0 {
		t.Errorf("negative label produced spans: %+v", result.Spans)
	}
	if len(progress) != 3 || progress[2].Frames != 3 || progress[2].Fraction != 1 {
		t.Errorf("progress = %+v", progress)
	}
}
