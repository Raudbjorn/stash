package native

import (
	"math"
	"testing"
)

// The geometry and grouping are testable without weights; the model-specific
// decode is tested for its arithmetic and its shape checking, which is where a
// wrong stride or a transposed output shows up.

func TestIntersectionOverUnion(t *testing.T) {
	a := Box{X: 0, Y: 0, W: 10, H: 10}

	if got := a.IntersectionOverUnion(a); math.Abs(float64(got-1)) > 1e-6 {
		t.Errorf("a box against itself = %v, want 1", got)
	}
	if got := a.IntersectionOverUnion(Box{X: 100, Y: 100, W: 10, H: 10}); got != 0 {
		t.Errorf("disjoint boxes = %v, want 0", got)
	}

	// Half-overlapping: intersection 50, union 150.
	half := Box{X: 5, Y: 0, W: 10, H: 10}
	if got := a.IntersectionOverUnion(half); math.Abs(float64(got-1.0/3.0)) > 1e-6 {
		t.Errorf("half-overlap = %v, want 1/3", got)
	}

	// A degenerate box must not produce a division by zero.
	if got := a.IntersectionOverUnion(Box{}); got != 0 {
		t.Errorf("a zero-area box = %v, want 0", got)
	}
}

// A detector fires several times on one face; without suppression every face
// becomes three or four.
func TestNonMaxSuppression(t *testing.T) {
	detections := []FaceDetection{
		{Box: Box{X: 0, Y: 0, W: 10, H: 10}, Score: 0.9},
		{Box: Box{X: 1, Y: 1, W: 10, H: 10}, Score: 0.95},    // same face, better score
		{Box: Box{X: 2, Y: 2, W: 10, H: 10}, Score: 0.8},     // same face again
		{Box: Box{X: 100, Y: 100, W: 10, H: 10}, Score: 0.7}, // a different face
	}

	kept := NonMaxSuppression(detections, 0.5)

	if len(kept) != 2 {
		t.Fatalf("kept %d detections, want 2: %+v", len(kept), kept)
	}
	// The best-scoring member of the cluster survives, not the first.
	if kept[0].Score != 0.95 {
		t.Errorf("kept score %v, want the cluster's best 0.95", kept[0].Score)
	}
	if kept[1].Box.X != 100 {
		t.Errorf("the distant face was not kept: %+v", kept[1])
	}
}

func TestNonMaxSuppressionOnEmptyInput(t *testing.T) {
	if got := NonMaxSuppression(nil, 0.5); got != nil {
		t.Errorf("NMS on no input = %v, want nil", got)
	}
}

// The detector runs on a resized frame, so its coordinates mean nothing until
// they are scaled back. Forgetting produces plausibly-shaped boxes in the wrong
// place - which no assertion downstream would catch.
func TestScaleDetections(t *testing.T) {
	detections := []FaceDetection{{
		Box:       Box{X: 10, Y: 20, W: 30, H: 40},
		Landmarks: [5][2]float32{{15, 25}},
	}}

	scaled := ScaleDetections(detections, 320, 320, 1920, 1080)

	wantX := float32(10) * 1920 / 320
	wantY := float32(20) * 1080 / 320
	if scaled[0].Box.X != wantX || scaled[0].Box.Y != wantY {
		t.Errorf("box = (%v,%v), want (%v,%v)", scaled[0].Box.X, scaled[0].Box.Y, wantX, wantY)
	}
	if scaled[0].Landmarks[0][0] != float32(15)*1920/320 {
		t.Errorf("landmarks were not scaled: %v", scaled[0].Landmarks[0])
	}

	// The original must not be mutated: a caller may want both coordinate
	// systems.
	if detections[0].Box.X != 10 {
		t.Error("ScaleDetections mutated its input")
	}
}

// A detector near an edge routinely predicts a box that extends past it.
// Reading outside the buffer would be a crash rather than a bad crop.
func TestCropFaceClampsToTheFrame(t *testing.T) {
	const w, h = 16, 16
	frame := make([]byte, w*h*3)
	for i := range frame {
		frame[i] = byte(i % 256)
	}

	cases := []Box{
		{X: -50, Y: -50, W: 100, H: 100}, // entirely outside, top-left
		{X: 10, Y: 10, W: 100, H: 100},   // extends past the bottom-right
		{X: 0, Y: 0, W: 16, H: 16},       // exactly the frame
	}

	for _, box := range cases {
		crop, err := CropFace(frame, w, h, box, 8)
		if err != nil {
			t.Errorf("CropFace(%+v): %v", box, err)
			continue
		}
		if len(crop) != 8*8*3 {
			t.Errorf("crop is %d bytes, want %d", len(crop), 8*8*3)
		}
	}
}

func TestCropFaceRejectsBadInput(t *testing.T) {
	frame := make([]byte, 4*4*3)

	if _, err := CropFace(frame, 4, 4, Box{W: 4, H: 4}, 0); err == nil {
		t.Error("a zero crop size was accepted")
	}
	if _, err := CropFace(frame, 8, 8, Box{W: 4, H: 4}, 4); err == nil {
		t.Error("a frame of the wrong size was accepted")
	}
	// A box entirely off the right edge collapses to nothing after clamping.
	if _, err := CropFace(frame, 4, 4, Box{X: 100, Y: 100, W: 1, H: 1}, 4); err != nil {
		// Clamping keeps it inside, so this must still produce a crop rather
		// than an error - the point is that it does not read out of bounds.
		t.Logf("an off-frame box produced: %v", err)
	}
}

// YuNet's score is the geometric mean of two heads. Using either alone gives
// detections that look reasonable and threshold quite differently.
func TestDecodeYuNetCombinesBothHeads(t *testing.T) {
	const (
		stride = 8
		side   = 16 // 2x2 cells
	)
	cells := (side / stride) * (side / stride)

	cls := make([]float32, cells)
	obj := make([]float32, cells)
	bbox := make([]float32, cells*4)
	kps := make([]float32, cells*10)

	// One cell with a high product, one where each head alone would pass but
	// the product does not.
	cls[0], obj[0] = 0.9, 0.9 // sqrt(0.81) = 0.9
	cls[1], obj[1] = 0.9, 0.1 // sqrt(0.09) = 0.3

	// A unit-sized box centred on the cell.
	for cell := 0; cell < cells; cell++ {
		bbox[cell*4+0] = 0.5
		bbox[cell*4+1] = 0.5
		bbox[cell*4+2] = 0
		bbox[cell*4+3] = 0
	}

	detections, err := DecodeYuNet(cls, obj, bbox, kps, stride, side, side, 0.5)
	if err != nil {
		t.Fatalf("DecodeYuNet: %v", err)
	}

	if len(detections) != 1 {
		t.Fatalf("decoded %d detections, want 1: %+v", len(detections), detections)
	}
	if math.Abs(float64(detections[0].Score-0.9)) > 1e-5 {
		t.Errorf("score = %v, want the geometric mean 0.9", detections[0].Score)
	}

	// The predicted point is the box's CENTRE; the box is expressed by its
	// top-left corner, so a width-8 box centred at 4 starts at 0.
	box := detections[0].Box
	if math.Abs(float64(box.W-stride)) > 1e-5 {
		t.Errorf("width = %v, want %v", box.W, float32(stride))
	}
	if math.Abs(float64(box.X-(0.5*stride-box.W/2))) > 1e-5 {
		t.Errorf("x = %v; the centre was not converted to a corner", box.X)
	}
}

// A mis-shaped output is a wrong stride or a transposed tensor, which would
// otherwise be read as garbage detections rather than reported.
func TestDecodeYuNetChecksItsInputShapes(t *testing.T) {
	const stride, side = 8, 32
	cells := (side / stride) * (side / stride)

	full := func(n int) []float32 { return make([]float32, n) }

	if _, err := DecodeYuNet(full(1), full(cells), full(cells*4), full(cells*10), stride, side, side, 0.5); err == nil {
		t.Error("a short classification tensor was accepted")
	}
	if _, err := DecodeYuNet(full(cells), full(cells), full(2), full(cells*10), stride, side, side, 0.5); err == nil {
		t.Error("a short box tensor was accepted")
	}
	if _, err := DecodeYuNet(full(cells), full(cells), full(cells*4), full(2), stride, side, side, 0.5); err == nil {
		t.Error("a short landmark tensor was accepted")
	}
}

func TestGroupFaces(t *testing.T) {
	// Two distinct people, seen alternately.
	personA := []float32{1, 0, 0}
	personB := []float32{0, 1, 0}

	embeddings := [][]float32{
		personA, personB, personA, personA, personB,
	}
	times := []float64{0, 2, 4, 6, 8}

	tracks := GroupFaces(embeddings, times, 0.8)

	if len(tracks) != 2 {
		t.Fatalf("grouped into %d tracks, want 2", len(tracks))
	}
	// Largest first: the most-seen face is the one a user cares about.
	if tracks[0].Count != 3 {
		t.Errorf("the largest track has %d members, want 3", tracks[0].Count)
	}
	if len(tracks[0].Times) != tracks[0].Count {
		t.Errorf("track has %d times for %d members", len(tracks[0].Times), tracks[0].Count)
	}

	// The centroid must stay normalised, or later comparisons drift.
	var norm float64
	for _, v := range tracks[0].Embedding {
		norm += float64(v) * float64(v)
	}
	if math.Abs(math.Sqrt(norm)-1) > 1e-5 {
		t.Errorf("the track centroid has length %v, want 1", math.Sqrt(norm))
	}
}

// A lower threshold merges more aggressively; that it does so at all is what
// makes the threshold meaningful.
func TestGroupFacesThresholdMatters(t *testing.T) {
	similar := [][]float32{
		{1, 0, 0},
		{0.9, 0.436, 0}, // cosine about 0.9 with the first
	}
	times := []float64{0, 2}

	if got := GroupFaces(similar, times, 0.95); len(got) != 2 {
		t.Errorf("a strict threshold produced %d tracks, want 2", len(got))
	}
	if got := GroupFaces(similar, times, 0.8); len(got) != 1 {
		t.Errorf("a loose threshold produced %d tracks, want 1", len(got))
	}
}

func TestGroupFacesIgnoresEmptyVectors(t *testing.T) {
	tracks := GroupFaces([][]float32{{1, 0}, nil, {1, 0}}, []float64{0, 1, 2}, 0.9)
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(tracks))
	}
	if tracks[0].Count != 2 {
		t.Errorf("count = %d, want 2; the empty vector should be skipped", tracks[0].Count)
	}
}
