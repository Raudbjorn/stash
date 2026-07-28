package native

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/onnx"
)

// The whole native path, end to end: a real video decoded by real ffmpeg,
// embedded by a real ONNX session, classified and collapsed into spans.
//
// The model is the small committed spike graph rather than SigLIP2, because a
// 370 MB download cannot be a test dependency. What that costs is meaningless
// embeddings; what it still proves is everything between them - the filter
// chain, the NCHW packing, the batching, the quantisation, the collapse - which
// is where the bugs are. Point AI_ORT_LIB at a libonnxruntime to run it.

func requireONNX(t *testing.T) {
	t.Helper()

	lib := os.Getenv("AI_ORT_LIB")
	if lib == "" {
		lib = os.Getenv("AI_SPIKE_ORT_LIB")
	}
	if lib == "" {
		t.Skip("set AI_ORT_LIB to a libonnxruntime shared library")
	}
	if err := onnx.Initialize(lib); err != nil {
		t.Skipf("could not load the ONNX runtime: %v", err)
	}
}

// spikeModel describes the committed test graph as a catalog entry.
//
// It takes 64x64x3 input and produces 10 outputs, so it stands in for an
// embedder of dimension 10. The dimensions must match the graph exactly: ONNX
// Runtime rejects a mismatch outright, which is the behaviour the catalog's
// explicit InputSize and Dim exist to get right.
const (
	spikeSide = 64
	spikeDim  = 10
)

func spikeModel() assets.Model {
	return assets.Model{
		Asset:     assets.Asset{Name: "tagging_spike.onnx"},
		Role:      assets.RoleEmbedding,
		Inputs:    []string{"input"},
		Outputs:   []string{"output"},
		InputSize: spikeSide,
		Dim:       spikeDim,
		Mean:      [3]float32{0.5, 0.5, 0.5},
		Std:       [3]float32{0.5, 0.5, 0.5},
	}
}

func spikeModelPath(t *testing.T) string {
	t.Helper()
	// The graph lives with the ONNX wrapper's tests; sharing it avoids a second
	// copy that could drift.
	path := filepath.Join("..", "..", "onnx", "testdata", "tagging_spike.onnx")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("spike model not found: %v", err)
	}
	return path
}

func newTestProvider(t *testing.T, head *Head) *Provider {
	t.Helper()
	requireONNX(t)

	ffmpeg := requireFFmpeg(t)

	embedder, err := NewEmbedder(spikeModel(), spikeModelPath(t), onnx.DefaultSessionOptions())
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}

	provider, err := New(Config{
		FFmpegPath:    ffmpeg,
		Embedder:      embedder,
		Head:          head,
		Category:      "actions",
		FrameInterval: 2,
		Threshold:     0.5,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { provider.Close() })
	return provider
}

// A head that fires on everything, so the pipeline below the model is what is
// being tested rather than the meaningless embeddings.
func alwaysFiringHead(t *testing.T, dim int) *Head {
	t.Helper()

	// A bias large enough that sigmoid saturates regardless of the input.
	weights := make([]float32, dim+1)
	weights[dim] = 50

	head, err := NewHead([]string{"Blowjob"}, dim, weights)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func TestNativeProviderProducesSpans(t *testing.T) {
	head := alwaysFiringHead(t, spikeDim)
	provider := newTestProvider(t, head)

	ffmpeg := requireFFmpeg(t)
	video := makeVideo(t, ffmpeg, 12, 160, 120)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	var reports []aitag.Progress
	result, err := provider.AnalyzeVideo(ctx, video, aitag.Options{FrameInterval: 2},
		func(p aitag.Progress) { reports = append(reports, p) })
	if err != nil {
		t.Fatalf("AnalyzeVideo: %v", err)
	}

	if result.SchemaVersion != 3 {
		t.Errorf("schema version = %d, want 3 to match the stored shape", result.SchemaVersion)
	}
	if result.FrameInterval != 2 {
		t.Errorf("frame interval = %v, want 2", result.FrameInterval)
	}
	if result.Duration < 11 || result.Duration > 13 {
		t.Errorf("duration = %v, want about 12", result.Duration)
	}

	spans := result.Spans["actions"]["Blowjob"]
	if len(spans) == 0 {
		t.Fatalf("a head that fires on every frame produced no spans: %+v", result.Spans)
	}

	// Because the confidences are quantized, a run of identical frames must
	// collapse to ONE span rather than one per frame. That is the whole point
	// of quantising, and its absence would be a silent hundredfold increase in
	// stored rows.
	if len(spans) != 1 {
		t.Errorf("identical frames produced %d spans, want 1: %+v", len(spans), spans)
	}
	if spans[0].Start != 0 {
		t.Errorf("first span starts at %v, want 0", spans[0].Start)
	}
	if spans[0].End == nil {
		t.Fatal("a multi-frame span was left open")
	}

	// Confidence must be present and quantized to two decimals.
	if spans[0].Confidence == nil {
		t.Fatal("the native provider reported no confidence")
	}
	conf := *spans[0].Confidence
	if conf != aitag.QuantizeConfidence(conf) {
		t.Errorf("confidence %v is not quantized; the collapse would never merge", conf)
	}

	if len(reports) == 0 {
		t.Error("no progress was reported")
	}
}

// Embeddings must be available without a head: that is how a library is
// prepared before there is anything trained to classify it with.
func TestNativeProviderEmbedsWithoutAHead(t *testing.T) {
	provider := newTestProvider(t, nil)

	ffmpeg := requireFFmpeg(t)
	video := makeVideo(t, ffmpeg, 8, 160, 120)

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	result, err := provider.AnalyzeVideo(ctx, video,
		aitag.Options{FrameInterval: 2, WantEmbeddings: true}, nil)
	if err != nil {
		t.Fatalf("AnalyzeVideo: %v", err)
	}

	if result.Embeddings == nil {
		t.Fatal("no embeddings were returned")
	}
	if result.Embeddings.Dim != spikeDim {
		t.Errorf("dim = %d, want %d", result.Embeddings.Dim, spikeDim)
	}
	if result.Embeddings.Count() != 4 {
		t.Errorf("embedded %d frames from an 8s clip at interval 2, want 4", result.Embeddings.Count())
	}
	if len(result.Embeddings.Times) != result.Embeddings.Count() {
		t.Errorf("times and vectors disagree: %d vs %d",
			len(result.Embeddings.Times), result.Embeddings.Count())
	}
	// The model name travels with the vectors: embeddings from different
	// models are not comparable.
	if result.Embeddings.Model == "" {
		t.Error("the embeddings do not name the model that produced them")
	}

	// No head means no spans, and that is a legitimate result rather than a
	// failure.
	if len(result.Spans) != 0 {
		t.Errorf("spans were produced without a head: %+v", result.Spans)
	}
}

// Capabilities must reflect what is actually configured, or the UI offers
// tagging that cannot run.
func TestNativeCapabilitiesReflectConfiguration(t *testing.T) {
	withHead := newTestProvider(t, alwaysFiringHead(t, spikeDim))
	if !withHead.Capabilities().Has(aitag.CapConfidence) {
		t.Error("a provider with a head does not claim confidences")
	}
	if !withHead.Capabilities().Has(aitag.CapEmbeddings) {
		t.Error("the native provider does not claim embeddings")
	}

	withoutHead := newTestProvider(t, nil)
	if withoutHead.Capabilities().Has(aitag.CapConfidence) {
		t.Error("a provider with no head claims confidences it cannot produce")
	}
	if err := withoutHead.Available(context.Background()); err == nil {
		t.Error("a provider with no head reports itself available for tagging")
	}
}

// Image tagging is not implemented natively; saying so beats returning an empty
// result a caller would read as "these images have no tags".
func TestNativeImageTaggingReportsItsAbsence(t *testing.T) {
	provider := newTestProvider(t, nil)

	_, err := provider.AnalyzeImages(context.Background(), []string{"/a.jpg"}, aitag.Options{})
	if err == nil {
		t.Fatal("AnalyzeImages silently returned nothing")
	}
}

func TestNativeProviderHonoursCancellation(t *testing.T) {
	provider := newTestProvider(t, alwaysFiringHead(t, spikeDim))

	ffmpeg := requireFFmpeg(t)
	video := makeVideo(t, ffmpeg, 30, 320, 240)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_, err := provider.AnalyzeVideo(ctx, video, aitag.Options{FrameInterval: 0.1}, nil)
	if err == nil {
		t.Fatal("a cancelled analysis reported success")
	}
}

func TestNativeProviderRequiresItsDependencies(t *testing.T) {
	if _, err := New(Config{Embedder: &Embedder{}}); err == nil {
		t.Error("a provider with no ffmpeg path was accepted")
	}
	if _, err := New(Config{FFmpegPath: "ffmpeg"}); err == nil {
		t.Error("a provider with no embedder was accepted")
	}
}

// Confidences must be quantized before the collapse. Unquantized, the merge
// rule's exact-equality test never passes and every frame becomes its own span.
func TestQuantizationEnablesMerging(t *testing.T) {
	rng := rand.New(rand.NewSource(7))

	var raw, quantized []aitag.Frame
	for i := 0; i < 20; i++ {
		// Values that differ only far below two decimal places, as consecutive
		// frames of a static shot would.
		value := 0.87 + rng.Float64()*1e-6

		rawConf := value
		raw = append(raw, aitag.Frame{
			Index:  float64(i) * 2,
			Labels: map[string][]aitag.Detection{"actions": {{Tag: "x", Confidence: &rawConf}}},
		})

		quantConf := aitag.QuantizeConfidence(value)
		quantized = append(quantized, aitag.Frame{
			Index:  float64(i) * 2,
			Labels: map[string][]aitag.Detection{"actions": {{Tag: "x", Confidence: &quantConf}}},
		})
	}

	rawSpans := aitag.CollapseFrames(raw, 2, 4)["actions"]["x"]
	quantSpans := aitag.CollapseFrames(quantized, 2, 4)["actions"]["x"]

	if len(rawSpans) != 20 {
		t.Errorf("unquantized confidences produced %d spans; expected one per frame", len(rawSpans))
	}
	if len(quantSpans) != 1 {
		t.Errorf("quantized confidences produced %d spans, want 1", len(quantSpans))
	}

	t.Logf("unquantized: %d spans, quantized: %d - a %dx difference in stored rows",
		len(rawSpans), len(quantSpans), len(rawSpans)/max(1, len(quantSpans)))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
