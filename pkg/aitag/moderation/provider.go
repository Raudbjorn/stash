// Package moderation drives OpenAI's moderation endpoint as a coarse remote
// gate.
//
// It is here because it is the ONE hosted option whose usage policy permits
// this content. Anthropic's policy prohibits it outright, and its own
// documentation says Claude flags explicit material regardless of prompt; no
// other vendor's policy could be confirmed as permitting it. The moderation
// endpoint is free, accepts images, and returns per-category confidences in
// [0,1] - which happens to be exactly the shape the rest of this pipeline
// already speaks.
//
// What it is NOT: a tagger. Six coarse categories, of which one ("sexual") is
// relevant here, against the dozens of fine-grained action labels the local
// trained head produces. Use it as a pre-filter or a second opinion, not as a
// replacement for pkg/aitag/native.
package moderation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/native"
)

// ProviderName labels results from this provider.
const ProviderName = "openai_moderation"

// Endpoint is OpenAI's moderation API.
const Endpoint = "https://api.openai.com/v1/moderations"

// DefaultModel is the image-capable moderation model.
//
// The text-only models silently ignore image input rather than refusing it, so
// naming the omni model explicitly is what stops a misconfiguration producing
// confident nothing.
const DefaultModel = "omni-moderation-latest"

// Limits published for the endpoint.
const (
	// maxImageBytes is the documented per-image cap.
	maxImageBytes = 20 << 20
	// defaultTimeout bounds one request. Moderation is fast; a slow response is
	// a stuck request rather than hard work.
	defaultTimeout = 60 * time.Second
	// defaultInterval samples sparsely on purpose. This is a rate-limited
	// remote API on a shared quota, so a frame every two seconds - what the
	// local pipeline uses - would exhaust it on a single scene.
	defaultInterval = 30.0
)

// ErrNoAPIKey reports a missing credential.
var ErrNoAPIKey = errors.New("no OpenAI API key is configured")

// Config configures the provider.
type Config struct {
	// APIKey authenticates to OpenAI. Required.
	APIKey string
	// Model overrides the moderation model.
	Model string
	// FFmpegPath extracts frames. Required for video.
	FFmpegPath string
	// FrameInterval is the sampling period in seconds. Deliberately coarse.
	FrameInterval float64
	// Threshold is the minimum category score to report.
	Threshold float64
	// Category is the label group these labels belong to.
	Category string
	// HTTP overrides the client, for tests.
	HTTP *http.Client
	// BaseURL overrides the endpoint, for tests.
	BaseURL string
}

// Provider implements aitag.Provider against OpenAI's moderation endpoint.
type Provider struct {
	cfg    Config
	client *http.Client
}

// New builds the provider.
func New(cfg Config) *Provider {
	if cfg.Model == "" {
		cfg.Model = DefaultModel
	}
	if cfg.Category == "" {
		cfg.Category = "moderation"
	}
	client := cfg.HTTP
	if client == nil {
		client = &http.Client{Timeout: defaultTimeout}
	}
	return &Provider{cfg: cfg, client: client}
}

func (p *Provider) Name() string { return ProviderName }

// Capabilities reports what this can do.
//
// CapConfidence is claimed because the endpoint really does return graded
// scores rather than a flag - which is why it is usable here at all.
func (p *Provider) Capabilities() aitag.Capability {
	return aitag.CapVideo | aitag.CapImages | aitag.CapConfidence
}

// Close releases idle connections.
func (p *Provider) Close() error {
	if transport, ok := p.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

// Available checks the credential with a trivial request.
func (p *Provider) Available(ctx context.Context) error {
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return ErrNoAPIKey
	}

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	// A text probe: cheapest possible call that still exercises auth and the
	// model name.
	_, err := p.moderate(ctx, []input{{Type: "text", Text: "ping"}})
	return err
}

// Models reports what would run.
func (p *Provider) Models(ctx context.Context) ([]aitag.ModelInfo, error) {
	return []aitag.ModelInfo{{
		Name:          p.cfg.Model,
		Categories:    []string{p.cfg.Category},
		Type:          "moderation",
		FrameInterval: p.frameInterval(aitag.Options{}),
		Threshold:     p.threshold(aitag.Options{}),
	}}, nil
}

// input is one element of the moderation request.
type input struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// response is what the endpoint returns.
type response struct {
	Results []struct {
		Flagged bool `json:"flagged"`
		// CategoryScores are the per-category confidences in [0,1], which is
		// what makes this usable as a graded signal rather than a flag.
		CategoryScores map[string]float64 `json:"category_scores"`
		Categories     map[string]bool    `json:"categories"`
	} `json:"results"`
}

// moderate posts inputs and returns the parsed response.
func (p *Provider) moderate(ctx context.Context, inputs []input) (*response, error) {
	body, err := json.Marshal(map[string]any{"model": p.cfg.Model, "input": inputs})
	if err != nil {
		return nil, err
	}

	endpoint := p.cfg.BaseURL
	if endpoint == "" {
		endpoint = Endpoint
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.cfg.APIKey)

	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("moderation request failed: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, p.statusError(resp)
	}

	var decoded response
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode moderation response: %w", err)
	}
	return &decoded, nil
}

// statusError turns a failure into something actionable.
//
// Rate limiting is called out by name because it is the failure this provider
// will actually hit: the free tier's image quota is small, and "429" alone
// sends the user looking for a bug rather than at their sampling interval.
func (p *Provider) statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	message := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &envelope) == nil && envelope.Error.Message != "" {
		message = envelope.Error.Message
	}

	switch resp.StatusCode {
	case http.StatusTooManyRequests:
		return fmt.Errorf(
			"the moderation API rate-limited this request (%s); increase the frame interval, "+
				"which for a remote API should be tens of seconds rather than the local pipeline's two",
			message)
	case http.StatusUnauthorized:
		return fmt.Errorf("the moderation API rejected the key: %s", message)
	default:
		return fmt.Errorf("the moderation API returned %d: %s", resp.StatusCode, truncate(message, 300))
	}
}

// AnalyzeVideo samples frames and moderates each.
//
// Sampled far more coarsely than the local pipeline: this is a rate-limited
// remote API, so it produces a scene-level signal rather than a per-frame one.
func (p *Provider) AnalyzeVideo(ctx context.Context, path string, opts aitag.Options, sink aitag.Sink) (*aitag.Result, error) {
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return nil, ErrNoAPIKey
	}
	if p.cfg.FFmpegPath == "" {
		return nil, errors.New("the moderation provider needs an ffmpeg path to sample frames")
	}

	interval := p.frameInterval(opts)
	threshold := p.threshold(opts)

	// 512 square: large enough for the model, small enough that a JPEG stays
	// far inside the 20 MB cap and the upload is not the bottleneck.
	frames, err := native.OpenFrames(ctx, p.cfg.FFmpegPath, path, native.ExtractOptions{
		Interval: interval,
		Size:     512,
		VR:       opts.VR,
		Resample: "bicubic",
	})
	if err != nil {
		return nil, err
	}
	defer frames.Close()

	result := &aitag.Result{
		SchemaVersion: 3,
		FrameInterval: interval,
		Spans:         aitag.SpansByCategory{},
		Models: []aitag.ModelInfo{{
			Name:          p.cfg.Model,
			Categories:    []string{p.cfg.Category},
			Type:          "moderation",
			FrameInterval: interval,
			Threshold:     threshold,
		}},
	}

	var (
		detected []aitag.Frame
		buf      []byte
		count    int
	)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		frame, err := frames.Next(buf)
		if err != nil {
			return nil, err
		}
		if frame == nil {
			break
		}
		buf = frame.RGB

		encoded, err := aitag.EncodePNG(frame.RGB, 512, 512)
		if err != nil {
			return nil, err
		}
		if len(encoded) > maxImageBytes {
			return nil, fmt.Errorf("a sampled frame is %d bytes, above the %d limit", len(encoded), maxImageBytes)
		}

		scores, err := p.moderateImage(ctx, encoded)
		if err != nil {
			return nil, err
		}

		var labels []aitag.Detection
		for category, score := range scores {
			if score < threshold {
				continue
			}
			// Quantized for the same reason the native provider quantizes: the
			// span collapse merges only on exactly equal confidences, so raw
			// scores would produce one span per frame.
			value := aitag.QuantizeConfidence(score)
			labels = append(labels, aitag.Detection{Tag: category, Confidence: &value})
		}

		if len(labels) > 0 {
			detected = append(detected, aitag.Frame{
				Index:  frame.Time,
				Labels: map[string][]aitag.Detection{p.cfg.Category: labels},
			})
		}

		count++
		sink.Report(aitag.Progress{
			Fraction: -1,
			Frames:   count,
			Message:  fmt.Sprintf("Moderated %d frames.", count),
		})
	}

	if count > 0 {
		result.Duration = float64(count) * interval
	}

	result.Spans = aitag.CollapseFrames(detected, interval, interval)
	aitag.SortSpans(result.Spans)
	return result, nil
}

// moderateImage sends one frame and returns its category scores.
func (p *Provider) moderateImage(ctx context.Context, png []byte) (map[string]float64, error) {
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)

	decoded, err := p.moderate(ctx, []input{{Type: "image_url", ImageURL: &imageURL{URL: dataURL}}})
	if err != nil {
		return nil, err
	}
	if len(decoded.Results) == 0 {
		return nil, errors.New("the moderation API returned no result for a frame")
	}
	return decoded.Results[0].CategoryScores, nil
}

// AnalyzeImages moderates still images.
func (p *Provider) AnalyzeImages(ctx context.Context, paths []string, opts aitag.Options) (*aitag.ImageResult, error) {
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return nil, ErrNoAPIKey
	}

	threshold := p.threshold(opts)
	out := &aitag.ImageResult{
		Tags:   map[string]map[string][]string{},
		Errors: map[string]string{},
		Models: []aitag.ModelInfo{{Name: p.cfg.Model, Categories: []string{p.cfg.Category}, Type: "moderation"}},
	}

	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		data, err := os.ReadFile(path)
		if err != nil {
			// Per-image, so one unreadable file does not lose the batch.
			out.Errors[path] = err.Error()
			continue
		}
		if len(data) > maxImageBytes {
			out.Errors[path] = fmt.Sprintf("image is %d bytes, above the %d limit", len(data), maxImageBytes)
			continue
		}

		mime := mimeFor(path)
		dataURL := fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(data))

		decoded, err := p.moderate(ctx, []input{{Type: "image_url", ImageURL: &imageURL{URL: dataURL}}})
		if err != nil {
			out.Errors[path] = err.Error()
			continue
		}
		if len(decoded.Results) == 0 {
			out.Errors[path] = "the moderation API returned no result"
			continue
		}

		var labels []string
		for category, score := range decoded.Results[0].CategoryScores {
			if score >= threshold {
				labels = append(labels, category)
			}
		}
		out.Tags[path] = map[string][]string{p.cfg.Category: labels}
	}

	if len(out.Errors) == 0 {
		out.Errors = nil
	}
	return out, nil
}

func mimeFor(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	default:
		return "image/jpeg"
	}
}

func (p *Provider) frameInterval(opts aitag.Options) float64 {
	if opts.FrameInterval > 0 {
		return opts.FrameInterval
	}
	if p.cfg.FrameInterval > 0 {
		return p.cfg.FrameInterval
	}
	return defaultInterval
}

func (p *Provider) threshold(opts aitag.Options) float64 {
	if opts.Threshold > 0 {
		return opts.Threshold
	}
	if p.cfg.Threshold > 0 {
		return p.cfg.Threshold
	}
	return 0.5
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var _ aitag.Provider = (*Provider)(nil)
