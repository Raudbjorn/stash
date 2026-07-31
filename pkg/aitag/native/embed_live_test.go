package native

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/onnx"
)

// The catalog's embedding contract - 768 dimensions at 224 pixels - is asserted
// everywhere as a NUMBER, and a number cannot be checked against a graph that
// nobody ran. This downloads the real weights and runs them.
//
// It catches the class of error that unit tests structurally cannot: a checksum
// proves the right bytes arrived, not that the graph inside them has the shape
// the catalog claims. A model whose Dim was recorded wrong loads cleanly, embeds
// cleanly, and returns vectors of the wrong length - which surfaces much later
// as a trained head that scores nonsense.
//
// Network-gated. Run when a model entry changes:
//
//	AI_LIVE_FETCH=1 AI_ORT_LIB=/path/to/libonnxruntime.so \
//	  go test ./pkg/aitag/native/ -run TestLiveEmbedder
//
// AI_EMBED_MODEL selects a catalog entry other than the default - use it to
// check an int8 or fp16 variant, whose graphs are separate exports and can
// disagree with the entry that describes them.
func TestLiveEmbedder(t *testing.T) {
	if os.Getenv("AI_LIVE_FETCH") == "" {
		t.Skip("set AI_LIVE_FETCH=1 to download the real weights and run them")
	}
	name := os.Getenv("AI_EMBED_MODEL")
	if name == "" {
		name = assets.DefaultEmbedder
	}
	model, ok := assets.FindModel(name)
	if !ok {
		t.Fatalf("%q is not in the catalog", name)
	}
	if !model.Downloadable() {
		t.Skipf("%s has no published artifact to fetch", name)
	}
	d := &assets.Downloader{Dir: os.Getenv("AI_FETCH_DIR")}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	path, err := d.Fetch(ctx, model.Asset, nil)
	if err != nil {
		t.Fatalf("fetch %s: %v", name, err)
	}
	if lib := os.Getenv("AI_ORT_LIB"); !onnx.Ready() {
		if lib == "" {
			t.Skip("set AI_ORT_LIB to the ONNX runtime library")
		}
		if err := onnx.Initialize(lib); err != nil {
			t.Fatalf("Initialize: %v", err)
		}
		defer onnx.Shutdown()
	}

	emb, err := NewEmbedder(model, path, onnx.DefaultSessionOptions())
	if err != nil {
		t.Fatalf("NewEmbedder: %v", err)
	}
	defer emb.Close()

	prep := NewPreprocessor(model)
	batch := prep.NewBatch(2)
	px := model.InputSize * model.InputSize * 3
	flat := make([]byte, px)
	if err := prep.Add(batch, &FrameData{Index: 0, Time: 0, RGB: flat}); err != nil {
		t.Fatal(err)
	}
	varied := make([]byte, px)
	for i := range varied {
		varied[i] = byte(i % 251)
	}
	if err := prep.Add(batch, &FrameData{Index: 1, Time: 2, RGB: varied}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	vecs, err := emb.EmbedBatch(batch, nil)
	if err != nil {
		t.Fatalf("EmbedBatch: %v", err)
	}
	if len(vecs) != 2*model.Dim {
		t.Fatalf("got %d floats, want %d (2 x %d-d); the catalog's declared "+
			"dimension does not match the graph", len(vecs), 2*model.Dim, model.Dim)
	}

	var norm float32
	for _, x := range vecs[model.Dim:] {
		norm += x * x
	}
	if norm == 0 {
		t.Error("the embedding is all zeros")
	}
	// Two different frames must not embed identically, which is what a
	// mis-wired preprocessor or a graph run on uninitialised input produces.
	same := true
	for i := 0; i < model.Dim; i++ {
		if vecs[i] != vecs[model.Dim+i] {
			same = false
			break
		}
	}
	if same {
		t.Error("two different frames produced identical embeddings")
	}
	t.Logf("%s: 2 frames -> %d-d each, |v|^2=%.2f, %v", name, model.Dim, norm, time.Since(start))
}
