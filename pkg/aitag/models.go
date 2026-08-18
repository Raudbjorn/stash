// Package aitag is Stash's AI video tagging pipeline.
//
// It has two implementations behind one Provider interface: a native in-process
// ONNX pipeline, and an HTTP client for an existing NSFW AI server. Both write
// through the same sink, so the recommenders and the UI cannot tell them apart
// and a library can be migrated scene by scene.
//
// This package must not import anything under internal/, so it stays usable
// from pkg/ and testable without standing up the AI server.
package aitag

import (
	"context"
	"time"
)

// Capability describes what a provider can do.
//
// A bitmask rather than a set of booleans so the UI can render "this provider
// cannot do faces" without knowing every capability that exists.
type Capability uint32

const (
	// CapVideo means the provider can analyse a video file.
	CapVideo Capability = 1 << iota
	// CapImages means it can analyse still images.
	CapImages
	// CapEmbeddings means it can return frame embeddings, which is what the
	// trained head and similarity search need.
	CapEmbeddings
	// CapFaces means it can detect and embed faces.
	CapFaces
	// CapAudio means it can tag the audio track.
	CapAudio
	// CapConfidence means its detections carry real confidences rather than
	// only a threshold decision. This matters: the span-merge rule compares
	// confidences for equality, so a provider that emits real ones produces
	// far more, shorter spans.
	CapConfidence
)

// Has reports whether every capability in want is present.
func (c Capability) Has(want Capability) bool { return c&want == want }

// String renders the capabilities for diagnostics.
func (c Capability) String() string {
	names := []struct {
		bit  Capability
		name string
	}{
		{CapVideo, "video"},
		{CapImages, "images"},
		{CapEmbeddings, "embeddings"},
		{CapFaces, "faces"},
		{CapAudio, "audio"},
		{CapConfidence, "confidence"},
	}

	out := ""
	for _, n := range names {
		if c&n.bit == 0 {
			continue
		}
		if out != "" {
			out += ","
		}
		out += n.name
	}
	if out == "" {
		return "none"
	}
	return out
}

// ModelInfo identifies a model that contributed to a result.
type ModelInfo struct {
	Name string `json:"name"`
	// Identifier is the provider's own numeric id, where it has one.
	Identifier *int     `json:"identifier,omitempty"`
	Version    *float64 `json:"version,omitempty"`
	// Categories are the label groups this model produces.
	Categories []string `json:"categories"`
	Type       string   `json:"type,omitempty"`
	// FrameInterval is how far apart this model's frames were sampled.
	FrameInterval float64 `json:"frame_interval,omitempty"`
	Threshold     float64 `json:"threshold,omitempty"`
	// Extra records provider-specific configuration needed to reproduce a run.
	Extra map[string]any `json:"extra,omitempty"`
}

// Span is one raw detection over a range of a video.
//
// End is a pointer because "matched exactly one frame" and "matched a run of
// frames" are genuinely different: the collapse below leaves End nil until a
// second frame extends the span, and every downstream duration adds one frame
// interval to account for the sample the span's last frame stands for.
type Span struct {
	Start float64  `json:"start"`
	End   *float64 `json:"end,omitempty"`
	// Confidence is nil when the provider only reported a threshold decision.
	Confidence *float64 `json:"confidence,omitempty"`
}

// EndOrStart returns the span's end, falling back to its start.
func (s Span) EndOrStart() float64 {
	if s.End != nil {
		return *s.End
	}
	return s.Start
}

// Duration is the span's covered time, including the frame it ends on.
func (s Span) Duration(frameInterval float64) float64 {
	return (s.EndOrStart() - s.Start) + frameInterval
}

// SpansByTag maps a label to its raw spans, in time order.
type SpansByTag map[string][]Span

// SpansByCategory maps a category to its labels' spans.
type SpansByCategory map[string]SpansByTag

// Result is a completed video analysis.
type Result struct {
	// SchemaVersion tracks the shape of Spans. 3 is the current NSFW AI server
	// format, which the native provider also produces so both feed the same
	// postprocessing.
	SchemaVersion int `json:"schema_version"`

	Duration      float64 `json:"duration"`
	FrameInterval float64 `json:"frame_interval"`

	Models []ModelInfo     `json:"models"`
	Spans  SpansByCategory `json:"timespans"`

	// Embeddings are per-frame vectors, present only when they were requested
	// and the provider supports them. Kept out of Spans because they are large
	// and are stored separately.
	Embeddings *Embeddings `json:"-"`

	// Metrics are timings the provider chose to report.
	Metrics map[string]any `json:"metrics,omitempty"`
}

// Embeddings is a frame-aligned block of vectors.
//
// Stored flat rather than as [][]float32: one allocation instead of thousands,
// which matters at 30 minutes of video and a frame every two seconds.
type Embeddings struct {
	// Dim is the vector width.
	Dim int
	// Times holds the timestamp of each frame, so an embedding can be located
	// in the video without recomputing the sampling.
	Times []float64
	// Data is len(Times)*Dim values, frame-major.
	Data []float32
	// Model names what produced them, since vectors from different models are
	// not comparable.
	Model string
}

// At returns the vector for frame i. The slice aliases Data.
func (e *Embeddings) At(i int) []float32 {
	return e.Data[i*e.Dim : (i+1)*e.Dim]
}

// Count is the number of frames.
func (e *Embeddings) Count() int {
	if e.Dim == 0 {
		return 0
	}
	return len(e.Data) / e.Dim
}

// ImageResult is a completed still-image analysis.
type ImageResult struct {
	// Tags maps a path to category -> labels.
	Tags   map[string]map[string][]string `json:"tags"`
	Models []ModelInfo                    `json:"models"`
	// Errors maps a path to why it could not be analysed. Per-path rather than
	// a single error: one unreadable file must not lose the whole batch.
	Errors map[string]string `json:"errors,omitempty"`
}

// Options configure an analysis.
type Options struct {
	// FrameInterval is the sampling period in seconds. Zero uses the
	// provider's default.
	FrameInterval float64
	// Threshold is the minimum confidence for a label. Zero uses the
	// provider's default.
	Threshold float64
	// VR marks 180/360 footage, which needs cropping before inference.
	VR bool
	// SkipCategories names label groups not to compute.
	SkipCategories []string
	// WantEmbeddings requests per-frame vectors.
	WantEmbeddings bool
	// UseVoyageReranker selects the configured external taxonomy reranker.
	// Nil preserves the provider's configured default.
	UseVoyageReranker *bool
}

// Progress is one incremental report from a running analysis.
type Progress struct {
	// Fraction is 0..1, or negative when the provider cannot estimate it.
	Fraction float64
	Message  string
	// Frames processed so far, where known.
	Frames int
}

// Sink receives progress during an analysis.
//
// A function rather than a channel so a provider that has nothing to report can
// pass nil, and a caller that does not care need not drain anything.
type Sink func(Progress)

// Report is a nil-safe way to emit progress.
func (s Sink) Report(p Progress) {
	if s != nil {
		s(p)
	}
}

// FrameClassifier is the optional narrow contract used by marker-grounded VLM
// evaluation. Labels are the complete configured taxonomy for every frame.
type FrameClassifier interface {
	Labels() []string
	ClassifyFrame(ctx context.Context, rgb []byte, width, height int) (map[string]bool, error)
}

// Provider analyses media.
//
// The interface is the centrepiece of workstream C rather than an afterthought:
// the proprietary engine cannot be ported, so an HTTP provider that drives it
// as-is and a native provider that replaces it must be interchangeable, and
// they must write identical results so a library can migrate scene by scene.
type Provider interface {
	// Name identifies the provider in results and settings.
	Name() string

	// Capabilities reports what it can do.
	Capabilities() Capability

	// Available reports whether it can run right now, with a reason if not.
	Available(ctx context.Context) error

	// Models lists what it would run.
	Models(ctx context.Context) ([]ModelInfo, error)

	// AnalyzeVideo analyses one file. Long-running: the context is the only
	// deadline, and cancellation must be honoured promptly.
	AnalyzeVideo(ctx context.Context, path string, opts Options, sink Sink) (*Result, error)

	// AnalyzeImages analyses still images.
	AnalyzeImages(ctx context.Context, paths []string, opts Options) (*ImageResult, error)

	// Close releases whatever the provider holds - sessions, sockets, models.
	Close() error
}

// Marker is a generated scene marker, the output of stage-two clustering.
type Marker struct {
	// Category and Tag are the provider's own names.
	Category string
	Tag      string
	// RenamedTag is what the marker is titled, after the category rules.
	RenamedTag string

	Start float64
	End   float64
	// TotalConfidence is the accumulated confidence-weighted duration. Not a
	// mean and not comparable across markers of different lengths; see
	// clustering.go for why it is what it is.
	TotalConfidence float64
}

// Duration is the marker's length, including the frame it ends on.
func (m Marker) Duration(frameInterval float64) float64 {
	return (m.End - m.Start) + frameInterval
}

// RunSummary describes a completed analysis for logging and history.
type RunSummary struct {
	Provider  string
	Path      string
	Duration  time.Duration
	Frames    int
	Spans     int
	Markers   int
	StartedAt time.Time
}
