// Package httpprov drives an existing NSFW AI server over its HTTP API.
//
// It exists because the proprietary engine cannot be ported: its wheel is
// closed, its weights are patron-gated, and its licence forbids reverse
// engineering and derivative works. Running it as-is over its own API is
// permitted for personal local use, which is exactly what this does.
//
// It also earns its keep during the native port: it produces the golden outputs
// the native pipeline is measured against, so accuracy regressions are visible
// rather than assumed.
package httpprov

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
)

// Defaults chosen to match the server's own, so an unconfigured provider
// behaves the way the standalone stack did.
const (
	defaultFrameInterval = 2.0
	defaultThreshold     = 0.5

	// analysisTimeout bounds a whole video request. Deliberately huge:
	// analysing a feature-length file in one call is the normal case, and a
	// shorter default would fail exactly the workloads this exists for.
	defaultAnalysisTimeout = 2 * time.Hour
	// dialTimeout distinguishes "the server is absent" from "the server is
	// working hard", which the readiness probe depends on.
	defaultDialTimeout = 10 * time.Second
	// probeTimeout bounds a readiness or model-list request.
	defaultProbeTimeout = 15 * time.Second
)

// Config configures the provider.
type Config struct {
	// ServerURL is the base URL of the NSFW AI server.
	ServerURL string
	// FrameInterval and Threshold default the same fields on Options.
	FrameInterval float64
	Threshold     float64
	// AnalysisTimeout bounds one video request. Zero uses the default.
	AnalysisTimeout time.Duration
	// HTTP overrides the client, for tests.
	HTTP *http.Client
}

// Provider implements aitag.Provider against a remote server.
type Provider struct {
	cfg    Config
	client *http.Client
}

// ProviderName is how results from this provider are labelled.
//
// It matches the service name the existing plugin ecosystem uses, so stored
// results remain attributable across the port.
const ProviderName = "skier_aitagging"

// New builds a provider.
func New(cfg Config) *Provider {
	client := cfg.HTTP
	if client == nil {
		timeout := cfg.AnalysisTimeout
		if timeout <= 0 {
			timeout = defaultAnalysisTimeout
		}
		client = &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				// A short dial timeout under a long overall timeout: connecting
				// should be instant, computing should not be rushed.
				DialContext:           (&net.Dialer{Timeout: defaultDialTimeout}).DialContext,
				ResponseHeaderTimeout: 0,
				MaxIdleConnsPerHost:   2,
			},
		}
	}
	return &Provider{cfg: cfg, client: client}
}

func (p *Provider) Name() string { return ProviderName }

// Capabilities reports what the remote server does.
//
// CapConfidence is absent deliberately: the v3 route hard-codes
// return_confidence=false, so every span comes back without one. Claiming
// otherwise would make the span-merge rule look broken downstream.
func (p *Provider) Capabilities() aitag.Capability {
	return aitag.CapVideo | aitag.CapImages
}

// Close releases idle connections. The provider holds nothing else.
func (p *Provider) Close() error {
	if transport, ok := p.client.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
	return nil
}

// ErrNoServer reports that no server URL is configured.
var ErrNoServer = errors.New("no AI server URL is configured")

// Available probes the server's readiness endpoint.
func (p *Provider) Available(ctx context.Context) error {
	if strings.TrimSpace(p.cfg.ServerURL) == "" {
		return ErrNoServer
	}

	ctx, cancel := context.WithTimeout(ctx, defaultProbeTimeout)
	defer cancel()

	req, err := p.newRequest(ctx, http.MethodGet, "/ready", nil)
	if err != nil {
		return err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("the AI server at %s is unreachable: %w", p.cfg.ServerURL, err)
	}
	defer drain(resp)

	// Anything below 400 counts as ready, matching the readiness FSM used for
	// remote services elsewhere: a server that answers at all is up.
	if resp.StatusCode >= 400 {
		return fmt.Errorf("the AI server at %s reported status %d", p.cfg.ServerURL, resp.StatusCode)
	}
	return nil
}

// modelInfo is the server's AIModelInfo.
type modelInfo struct {
	Name       string   `json:"name"`
	Identifier *int     `json:"identifier"`
	Version    *float64 `json:"version"`
	Categories []string `json:"categories"`
	Type       string   `json:"type"`
}

func (m modelInfo) toAITag(frameInterval, threshold float64) aitag.ModelInfo {
	return aitag.ModelInfo{
		Name:          m.Name,
		Identifier:    m.Identifier,
		Version:       m.Version,
		Categories:    m.Categories,
		Type:          m.Type,
		FrameInterval: frameInterval,
		Threshold:     threshold,
	}
}

// Models lists what the server would run.
func (p *Provider) Models(ctx context.Context) ([]aitag.ModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultProbeTimeout)
	defer cancel()

	req, err := p.newRequest(ctx, http.MethodGet, "/v3/current_ai_models/", nil)
	if err != nil {
		return nil, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer drain(resp)

	if resp.StatusCode != http.StatusOK {
		return nil, p.statusError(resp)
	}

	var raw []modelInfo
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}

	out := make([]aitag.ModelInfo, 0, len(raw))
	for _, m := range raw {
		out = append(out, m.toAITag(p.frameInterval(aitag.Options{}), p.threshold(aitag.Options{})))
	}
	return out, nil
}

// videoRequest is VideoRequestV3.
type videoRequest struct {
	Path             string   `json:"path"`
	FrameInterval    float64  `json:"frame_interval,omitempty"`
	Threshold        float64  `json:"threshold,omitempty"`
	VRVideo          bool     `json:"vr_video"`
	CategoriesToSkip []string `json:"categories_to_skip,omitempty"`
}

// videoResponse is what /v3/process_video/ returns: the AIVideoResultV3 under
// "result", plus timing metrics.
type videoResponse struct {
	Result struct {
		SchemaVersion int                                `json:"schema_version"`
		Duration      float64                            `json:"duration"`
		FrameInterval float64                            `json:"frame_interval"`
		Models        []modelInfo                        `json:"models"`
		Timespans     map[string]map[string][]serverSpan `json:"timespans"`
	} `json:"result"`
	Metrics map[string]any `json:"metrics"`
}

// serverSpan is TagTimeFrame.
type serverSpan struct {
	Start      float64  `json:"start"`
	End        *float64 `json:"end"`
	Confidence *float64 `json:"confidence"`
}

// AnalyzeVideo runs the remote pipeline over one file.
//
// The server does its own stage-one collapse, so what comes back is already raw
// spans in the shape the store expects; the marker clustering still happens
// locally, on the writeback path.
func (p *Provider) AnalyzeVideo(ctx context.Context, path string, opts aitag.Options, sink aitag.Sink) (*aitag.Result, error) {
	if strings.TrimSpace(p.cfg.ServerURL) == "" {
		return nil, ErrNoServer
	}

	// Only one progress report is possible: the API is a single blocking call
	// with no streaming. Saying so once is better than a fake progress bar.
	sink.Report(aitag.Progress{
		Fraction: -1,
		Message:  "Analysing on the AI server; it reports no incremental progress.",
	})

	body := videoRequest{
		Path:             path,
		FrameInterval:    p.frameInterval(opts),
		Threshold:        p.threshold(opts),
		VRVideo:          opts.VR,
		CategoriesToSkip: opts.SkipCategories,
	}

	var decoded videoResponse
	if err := p.postJSON(ctx, "/v3/process_video/", body, &decoded); err != nil {
		return nil, err
	}

	frameInterval := decoded.Result.FrameInterval
	if frameInterval <= 0 {
		frameInterval = body.FrameInterval
	}

	result := &aitag.Result{
		SchemaVersion: decoded.Result.SchemaVersion,
		Duration:      decoded.Result.Duration,
		FrameInterval: frameInterval,
		Spans:         aitag.SpansByCategory{},
		Metrics:       decoded.Metrics,
	}

	for _, m := range decoded.Result.Models {
		result.Models = append(result.Models, m.toAITag(frameInterval, body.Threshold))
	}

	for category, byTag := range decoded.Result.Timespans {
		spans := aitag.SpansByTag{}
		for tag, list := range byTag {
			converted := make([]aitag.Span, 0, len(list))
			for _, s := range list {
				converted = append(converted, aitag.Span{
					Start:      s.Start,
					End:        s.End,
					Confidence: s.Confidence,
				})
			}
			spans[tag] = converted
		}
		result.Spans[category] = spans
	}

	// The server's ordering is not guaranteed, and clustering walks spans in
	// order; sorting here means a reordered response cannot silently produce
	// overlapping markers.
	aitag.SortSpans(result.Spans)

	sink.Report(aitag.Progress{
		Fraction: 1,
		Message:  fmt.Sprintf("Analysed %d spans.", aitag.CountSpans(result.Spans)),
	})
	return result, nil
}

// imageRequest is ImageRequestV3.
type imageRequest struct {
	Paths            []string `json:"paths"`
	Threshold        float64  `json:"threshold,omitempty"`
	ReturnConfidence bool     `json:"return_confidence"`
}

// AnalyzeImages runs the remote pipeline over still images.
func (p *Provider) AnalyzeImages(ctx context.Context, paths []string, opts aitag.Options) (*aitag.ImageResult, error) {
	if strings.TrimSpace(p.cfg.ServerURL) == "" {
		return nil, ErrNoServer
	}
	if len(paths) == 0 {
		return &aitag.ImageResult{Tags: map[string]map[string][]string{}}, nil
	}

	body := imageRequest{Paths: paths, Threshold: p.threshold(opts)}

	// The result element shape is per-image and loosely typed: a category to a
	// list of labels, or {"error": "..."} when that image failed.
	var decoded struct {
		Result  []map[string]any `json:"result"`
		Models  []modelInfo      `json:"models"`
		Metrics map[string]any   `json:"metrics"`
	}
	if err := p.postJSON(ctx, "/v3/process_images/", body, &decoded); err != nil {
		return nil, err
	}

	out := &aitag.ImageResult{
		Tags:   map[string]map[string][]string{},
		Errors: map[string]string{},
	}
	for _, m := range decoded.Models {
		out.Models = append(out.Models, m.toAITag(0, body.Threshold))
	}

	for i, entry := range decoded.Result {
		if i >= len(paths) {
			break
		}
		path := paths[i]

		// A per-image failure is recorded rather than raised: one unreadable
		// file must not lose the rest of the batch.
		if message, isError := entry["error"].(string); isError {
			out.Errors[path] = message
			continue
		}

		tags := map[string][]string{}
		for category, value := range entry {
			list, ok := value.([]any)
			if !ok {
				continue
			}
			for _, item := range list {
				switch typed := item.(type) {
				case string:
					tags[category] = append(tags[category], typed)
				case []any:
					// (name, confidence) when the server was asked for them.
					if len(typed) > 0 {
						if name, ok := typed[0].(string); ok {
							tags[category] = append(tags[category], name)
						}
					}
				}
			}
		}
		out.Tags[path] = tags
	}

	if len(out.Errors) == 0 {
		out.Errors = nil
	}
	return out, nil
}

// ------------------------------------------------------------- transport ----

func (p *Provider) frameInterval(opts aitag.Options) float64 {
	if opts.FrameInterval > 0 {
		return opts.FrameInterval
	}
	if p.cfg.FrameInterval > 0 {
		return p.cfg.FrameInterval
	}
	return defaultFrameInterval
}

func (p *Provider) threshold(opts aitag.Options) float64 {
	if opts.Threshold > 0 {
		return opts.Threshold
	}
	if p.cfg.Threshold > 0 {
		return p.cfg.Threshold
	}
	return defaultThreshold
}

func (p *Provider) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	target, err := joinURL(p.cfg.ServerURL, path)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (p *Provider) postJSON(ctx context.Context, path string, body, into any) error {
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := p.newRequest(ctx, http.MethodPost, path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		// A cancelled analysis is not a server failure, and reporting it as one
		// would put a spurious error on a task the user stopped themselves.
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("AI server request to %s failed: %w", path, err)
	}
	defer drain(resp)

	if resp.StatusCode != http.StatusOK {
		return p.statusError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("decode %s response: %w", path, err)
	}
	return nil
}

// statusError turns a failure response into something worth showing a user.
//
// FastAPI puts the real reason in {"detail": ...}, so a bare status code would
// throw away the only useful part.
func (p *Provider) statusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	var envelope struct {
		Detail any `json:"detail"`
	}
	if json.Unmarshal(body, &envelope) == nil && envelope.Detail != nil {
		if text, ok := envelope.Detail.(string); ok {
			return fmt.Errorf("the AI server rejected the request (%d): %s", resp.StatusCode, text)
		}
		return fmt.Errorf("the AI server rejected the request (%d): %v", resp.StatusCode, envelope.Detail)
	}

	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return fmt.Errorf("the AI server returned status %d", resp.StatusCode)
	}
	return fmt.Errorf("the AI server returned status %d: %s", resp.StatusCode, truncate(trimmed, 300))
}

// drain consumes and closes a response body so the connection can be reused.
func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	_ = resp.Body.Close()
}

func joinURL(base, path string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", fmt.Errorf("invalid AI server URL %q: %w", base, err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + path
	return parsed.String(), nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

var _ aitag.Provider = (*Provider)(nil)
