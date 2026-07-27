package native

import (
	"math"
	"testing"

	"github.com/stashapp/stash/pkg/aitag/assets"
)

// The preprocessing conventions - channel order, NCHW layout, normalisation -
// are three independent ways to produce embeddings that look plausible and are
// wrong. None of them errors, so each is asserted directly.

func TestPreprocessProducesNCHW(t *testing.T) {
	prep := &Preprocessor{Size: 2, Mean: [3]float32{0, 0, 0}, Std: [3]float32{1, 1, 1}}
	batch := prep.NewBatch(1)

	// Four pixels with distinct channel values, so a transposed or interleaved
	// layout is visible rather than merely wrong.
	frame := &FrameData{Index: 0, Time: 0, RGB: []byte{
		255, 0, 0, // red
		0, 255, 0, // green
		0, 0, 255, // blue
		255, 255, 255, // white
	}}
	if err := prep.Add(batch, frame); err != nil {
		t.Fatalf("Add: %v", err)
	}

	data := batch.Tensor.Data
	plane := 4

	// Channel-major: the R plane first, then G, then B. Interleaved input
	// written straight through would put (255,0,0) in the first three slots.
	wantR := []float32{1, 0, 0, 1}
	wantG := []float32{0, 1, 0, 1}
	wantB := []float32{0, 0, 1, 1}

	for i := 0; i < plane; i++ {
		if data[0*plane+i] != wantR[i] {
			t.Errorf("R plane [%d] = %v, want %v", i, data[0*plane+i], wantR[i])
		}
		if data[1*plane+i] != wantG[i] {
			t.Errorf("G plane [%d] = %v, want %v", i, data[1*plane+i], wantG[i])
		}
		if data[2*plane+i] != wantB[i] {
			t.Errorf("B plane [%d] = %v, want %v", i, data[2*plane+i], wantB[i])
		}
	}
}

// SigLIP normalises to [-1,1]; ImageNet statistics would be a silent accuracy
// loss rather than an error.
func TestNormalisationMatchesTheModel(t *testing.T) {
	// SigLIP2, and v1 alongside it: both normalise to [-1,1], so this asserts
	// the shared convention against whichever is the default embedder.
	model, ok := assets.FindModel(assets.DefaultEmbedder)
	if !ok {
		t.Fatalf("the default embedder %q is not in the catalog", assets.DefaultEmbedder)
	}

	prep := NewPreprocessor(model)
	prep.Size = 1
	batch := prep.NewBatch(1)

	// Mid-grey must map to zero, black to -1, white to +1.
	cases := []struct {
		value byte
		want  float32
	}{{0, -1}, {128, (128.0/255 - 0.5) / 0.5}, {255, 1}}

	for _, tc := range cases {
		batch.Reset()
		if err := prep.Add(batch, &FrameData{RGB: []byte{tc.value, tc.value, tc.value}}); err != nil {
			t.Fatal(err)
		}
		if got := batch.Tensor.Data[0]; math.Abs(float64(got-tc.want)) > 1e-6 {
			t.Errorf("value %d normalised to %v, want %v", tc.value, got, tc.want)
		}
	}
}

// A zero standard deviation would divide by zero and fill the tensor with
// infinities that propagate through the whole graph.
func TestZeroStdIsGuarded(t *testing.T) {
	prep := NewPreprocessor(assets.Model{InputSize: 1, Std: [3]float32{0, 0, 0}})
	batch := prep.NewBatch(1)

	if err := prep.Add(batch, &FrameData{RGB: []byte{128, 128, 128}}); err != nil {
		t.Fatal(err)
	}
	for i, v := range batch.Tensor.Data {
		if math.IsInf(float64(v), 0) || math.IsNaN(float64(v)) {
			t.Fatalf("value %d is %v; a zero std was not guarded", i, v)
		}
	}
}

// The last batch of a scene is usually short, and the model must be told the
// real row count or it reads uninitialised rows and returns embeddings for
// frames that do not exist.
func TestTrimTensorCoversOnlyOccupiedRows(t *testing.T) {
	prep := &Preprocessor{Size: 2, Std: [3]float32{1, 1, 1}}
	batch := prep.NewBatch(4)

	for i := 0; i < 2; i++ {
		if err := prep.Add(batch, &FrameData{Index: i, Time: float64(i), RGB: make([]byte, 12)}); err != nil {
			t.Fatal(err)
		}
	}

	shape := prep.InputShape(batch)
	if shape[0] != 2 {
		t.Errorf("batch dimension = %d, want 2", shape[0])
	}

	trimmed := prep.TrimTensor(batch)
	if want := 2 * 3 * 4; len(trimmed.Data) != want {
		t.Errorf("trimmed tensor has %d values, want %d", len(trimmed.Data), want)
	}
}

func TestAddRejectsAWrongSizedFrame(t *testing.T) {
	prep := &Preprocessor{Size: 4, Std: [3]float32{1, 1, 1}}
	batch := prep.NewBatch(1)

	if err := prep.Add(batch, &FrameData{RGB: make([]byte, 10)}); err == nil {
		t.Error("a frame of the wrong size was accepted")
	}
}

func TestAddRejectsAFullBatch(t *testing.T) {
	prep := &Preprocessor{Size: 1, Std: [3]float32{1, 1, 1}}
	batch := prep.NewBatch(1)

	if err := prep.Add(batch, &FrameData{RGB: []byte{0, 0, 0}}); err != nil {
		t.Fatal(err)
	}
	if err := prep.Add(batch, &FrameData{RGB: []byte{0, 0, 0}}); err == nil {
		t.Error("a full batch accepted another frame; it would write past its tensor")
	}
}

func TestBatchSizeFromMemoryBudget(t *testing.T) {
	// A 224 frame is 224*224*3*4 = 602k, tripled for activations is ~1.8 MB.
	if got := BatchSizeFor(64<<20, 224); got < 20 || got > 64 {
		t.Errorf("64 MiB budget gave batch %d, expected roughly 30", got)
	}
	// A tiny budget must still produce a usable batch rather than zero.
	if got := BatchSizeFor(1, 224); got != 1 {
		t.Errorf("a 1-byte budget gave batch %d, want 1", got)
	}
	// And a huge one must be capped: past this the gains flatten and
	// cancellation stops feeling responsive.
	if got := BatchSizeFor(64<<30, 224); got != 64 {
		t.Errorf("an enormous budget gave batch %d, want the cap of 64", got)
	}
}
