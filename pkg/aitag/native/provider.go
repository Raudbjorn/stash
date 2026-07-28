package native

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/onnx"
)

// The native provider: decode once, embed, classify, collapse.
//
// It produces exactly the shape the HTTP provider does, so the store, the
// clustering and the writeback cannot tell them apart. That interchangeability
// is the point - it is what lets a library migrate scene by scene and lets the
// remote engine stay the accuracy reference while this one is measured.

// ProviderName labels results from this provider.
const ProviderName = "native"

// Defaults matching the remote engine's, so switching provider does not
// silently change the sampling.
const (
	defaultFrameInterval = 2.0
	defaultThreshold     = 0.5
	defaultCategory      = "actions"
)

// Config configures the native provider.
type Config struct {
	// FFmpegPath is the ffmpeg binary. Required.
	FFmpegPath string

	// Embedder produces frame vectors. Required.
	Embedder *Embedder

	// Head classifies those vectors. Optional: without one the provider still
	// produces embeddings, which is enough to train a head from.
	Head *Head

	// Category is the label group the head's outputs belong to, matching the
	// category rules used for markers.
	Category string

	// FrameInterval and Threshold default the same fields on Options.
	FrameInterval float64
	Threshold     float64

	// MemoryBudget bounds the batch tensor. Zero uses a conservative default.
	MemoryBudget int64

	// Concurrency bounds concurrent analyses. One by default: the parallelism
	// is already inside the ONNX kernels, and two scenes at once on the same
	// cores is slower than one after the other.
	Concurrency int
}

// Provider implements aitag.Provider in-process.
type Provider struct {
	cfg Config

	// slots bounds concurrent analyses.
	slots chan struct{}

	mu     sync.RWMutex
	closed bool
}

// New builds the native provider.
func New(cfg Config) (*Provider, error) {
	if cfg.FFmpegPath == "" {
		return nil, errors.New("the native provider needs an ffmpeg path")
	}
	if cfg.Embedder == nil {
		return nil, errors.New("the native provider needs an embedding model")
	}
	if cfg.Category == "" {
		cfg.Category = defaultCategory
	}

	concurrency := cfg.Concurrency
	if concurrency <= 0 {
		concurrency = 1
	}

	return &Provider{cfg: cfg, slots: make(chan struct{}, concurrency)}, nil
}

func (p *Provider) Name() string { return ProviderName }

// Capabilities reports what this build can do.
//
// CapConfidence is claimed because a head produces real per-frame scores - and
// that has a consequence the span collapse depends on: real confidences almost
// never compare equal, so they are quantized before collapsing or every frame
// becomes its own span.
func (p *Provider) Capabilities() aitag.Capability {
	caps := aitag.CapVideo | aitag.CapEmbeddings
	if p.head() != nil {
		caps |= aitag.CapConfidence
	}
	return caps
}

// Available reports whether the provider can run.
func (p *Provider) Available(ctx context.Context) error {
	p.mu.RLock()
	closed := p.closed
	p.mu.RUnlock()
	if closed {
		return errors.New("the native provider is closed")
	}

	if !onnx.Ready() {
		return fmt.Errorf("the ONNX runtime is not installed; download it in the AI settings")
	}
	if _, err := exec.LookPath(p.cfg.FFmpegPath); err != nil {
		return fmt.Errorf("ffmpeg is not available at %s: %w", p.cfg.FFmpegPath, err)
	}
	if p.head() == nil {
		return fmt.Errorf("no tagging head is configured; the provider can only produce embeddings")
	}
	return nil
}

// Models reports what would run.
func (p *Provider) Models(ctx context.Context) ([]aitag.ModelInfo, error) {
	interval := p.cfg.FrameInterval
	if interval <= 0 {
		interval = defaultFrameInterval
	}

	models := []aitag.ModelInfo{{
		Name:          p.cfg.Embedder.Name(),
		Categories:    []string{p.cfg.Category},
		Type:          "embedding",
		FrameInterval: interval,
		Threshold:     p.threshold(aitag.Options{}),
	}}
	return models, nil
}

// Close releases the models.
func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.cfg.Embedder.Close()
}

// SetHead attaches or replaces the classifier.
//
// Separate from construction because a trained head lives in the AI database,
// which is not open when the provider is first assembled - and because
// retraining should take effect without rebuilding the ONNX session, which is
// the expensive part.
func (p *Provider) SetHead(head *Head) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cfg.Head = head
}

func (p *Provider) head() *Head {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.cfg.Head
}

// AnalyzeVideo runs the native pipeline over one file.
func (p *Provider) AnalyzeVideo(ctx context.Context, path string, opts aitag.Options, sink aitag.Sink) (*aitag.Result, error) {
	head := p.head()
	if err := p.Available(ctx); err != nil && head != nil {
		return nil, err
	}

	// One scene at a time by default. Two concurrent analyses on the same cores
	// finish later than two sequential ones, and use twice the memory doing it.
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	started := time.Now()
	interval := p.frameInterval(opts)
	threshold := float32(p.threshold(opts))

	duration, err := probeDuration(ctx, p.cfg.FFmpegPath, path)
	if err != nil {
		// A missing duration costs a progress estimate, not the analysis.
		duration = 0
	}

	expectedFrames := 0
	if duration > 0 {
		expectedFrames = int(duration/interval) + 1
	}

	frames, err := OpenFrames(ctx, p.cfg.FFmpegPath, path, ExtractOptions{
		Interval: interval,
		Size:     p.cfg.Embedder.InputSize(),
		VR:       opts.VR,
		Resample: p.cfg.Embedder.Resample(),
	})
	if err != nil {
		return nil, err
	}
	defer frames.Close()

	batchSize := BatchSizeFor(p.cfg.MemoryBudget, p.cfg.Embedder.InputSize())

	embeddings, err := p.cfg.Embedder.EmbedFrames(ctx, frames, batchSize, sink, expectedFrames)
	if err != nil {
		return nil, err
	}

	result := &aitag.Result{
		SchemaVersion: 3,
		Duration:      duration,
		FrameInterval: interval,
		Spans:         aitag.SpansByCategory{},
		Models: []aitag.ModelInfo{{
			Name:          p.cfg.Embedder.Name(),
			Categories:    []string{p.cfg.Category},
			Type:          "embedding",
			FrameInterval: interval,
			Threshold:     float64(threshold),
		}},
		Metrics: map[string]any{
			"frames":           len(embeddings.Times),
			"embed_seconds":    time.Since(started).Seconds(),
			"batch_size":       batchSize,
			"intra_op_threads": onnx.PhysicalCores(),
		},
	}

	// A duration ffprobe could not supply is recovered from the frames, so a
	// file with a broken header still produces sane marker bounds.
	if result.Duration == 0 && len(embeddings.Times) > 0 {
		result.Duration = embeddings.Times[len(embeddings.Times)-1] + interval
	}

	if opts.WantEmbeddings {
		result.Embeddings = embeddings
	}

	if head == nil {
		// Embeddings without a head is a legitimate mode: it is how a library
		// is prepared before a head has been trained from it.
		return result, nil
	}

	// Cosine similarity needs unit vectors, and a zero-shot head's weights are
	// already normalised.
	NormalizeInPlace(embeddings.Data, embeddings.Dim)

	detected := make([]aitag.Frame, 0, embeddings.Count())
	var scratch []float32

	for i := 0; i < embeddings.Count(); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		hits, next, err := head.Apply(embeddings.At(i), threshold, scratch)
		scratch = next
		if err != nil {
			return nil, err
		}
		if len(hits) == 0 {
			continue
		}

		labels := make([]aitag.Detection, 0, len(hits))
		for _, hit := range hits {
			// Quantized to two decimals before the collapse. Without this the
			// merge rule's exact-equality test never passes and every frame
			// becomes its own span - a silent hundredfold increase in stored
			// rows that looks like a data explosion rather than a bug.
			confidence := aitag.QuantizeConfidence(float64(hit.Confidence))
			labels = append(labels, aitag.Detection{Tag: hit.Label, Confidence: &confidence})
		}

		detected = append(detected, aitag.Frame{
			Index:  embeddings.Times[i],
			Labels: map[string][]aitag.Detection{p.cfg.Category: labels},
		})
	}

	// Stage one, run locally: the remote provider's server does this itself, so
	// running it here is what makes the two produce the same stored shape.
	result.Spans = aitag.CollapseFrames(detected, interval, defaultMergeSeconds)
	aitag.SortSpans(result.Spans)

	result.Metrics["total_seconds"] = time.Since(started).Seconds()
	return result, nil
}

// defaultMergeSeconds is the stage-one gap tolerance. The upstream code
// defaults to 2 but its shipped configuration overrides it to 4, and matching
// the deployed behaviour matters more than matching the default.
const defaultMergeSeconds = 4.0

// AnalyzeImages is not implemented natively yet.
//
// Reported rather than silently returning nothing: a caller that asks for image
// tagging and receives an empty result would conclude its images are untagged.
func (p *Provider) AnalyzeImages(ctx context.Context, paths []string, opts aitag.Options) (*aitag.ImageResult, error) {
	return nil, errors.New("the native provider does not tag still images yet; use the HTTP provider")
}

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

var _ aitag.Provider = (*Provider)(nil)

// unused keeps the assets import meaningful for callers building a provider
// from a catalog entry.
var _ = assets.Model{}
