package native

import (
	"encoding/json"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/onnx"
)

// The face detector, checked against OpenCV's own FaceDetectorYN.
//
// The 233 KB MIT-licensed model is committed, so this runs without a download.
// testdata/yunet_reference.json holds what OpenCV produced on the same fixture;
// reproducing it is the only way to know the anchor decode, the stride layout
// and the channel order are all right, since each of the three is wrong in a
// way that still yields plausible boxes.

type yunetReference struct {
	InputSize      int     `json:"input_size"`
	ScoreThreshold float32 `json:"score_threshold"`
	ChannelOrder   string  `json:"channel_order"`
	Box            struct {
		X, Y, W, H float64
	} `json:"box"`
	Score     float64      `json:"score"`
	Landmarks [][2]float64 `json:"landmarks"`
}

func loadYuNetReference(t *testing.T) yunetReference {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("testdata", "yunet_reference.json"))
	if err != nil {
		t.Fatalf("read reference: %v", err)
	}
	var ref yunetReference
	if err := json.Unmarshal(data, &ref); err != nil {
		t.Fatalf("decode reference: %v", err)
	}
	return ref
}

// loadFixtureFrame reads the committed PNG as an RGB frame.
func loadFixtureFrame(t *testing.T, size int) *FrameData {
	t.Helper()

	file, err := os.Open(filepath.Join("testdata", "face.png"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() != size || bounds.Dy() != size {
		t.Fatalf("fixture is %dx%d, expected %dx%d", bounds.Dx(), bounds.Dy(), size, size)
	}

	rgb := make([]byte, size*size*3)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			// image/png returns 16-bit components; the model wants 8.
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			i := (y*size + x) * 3
			rgb[i+0] = byte(r >> 8)
			rgb[i+1] = byte(g >> 8)
			rgb[i+2] = byte(b >> 8)
		}
	}
	return &FrameData{RGB: rgb}
}

func yunetModel(t *testing.T) (assets.Model, string) {
	t.Helper()

	model, ok := assets.FindModel("face_detection_yunet_2023mar.onnx")
	if !ok {
		t.Fatal("YuNet is not in the catalog")
	}

	path := filepath.Join("testdata", model.Name)
	if _, err := os.Stat(path); err != nil {
		t.Skipf("YuNet fixture missing: %v", err)
	}
	return model, path
}

// The headline: the Go pipeline must reproduce OpenCV's detection.
func TestFaceDetectorMatchesOpenCV(t *testing.T) {
	requireONNX(t)

	model, path := yunetModel(t)
	ref := loadYuNetReference(t)

	detector, err := NewFaceDetector(model, path, onnx.DefaultSessionOptions())
	if err != nil {
		t.Fatalf("NewFaceDetector: %v", err)
	}
	defer detector.Close()
	detector.ScoreThreshold = ref.ScoreThreshold

	frame := loadFixtureFrame(t, model.InputSize)

	// Zero source dimensions: compare in the model's own coordinate space,
	// which is what OpenCV reported.
	faces, err := detector.Detect(frame, 0, 0)
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}

	if len(faces) != 1 {
		t.Fatalf("detected %d faces, OpenCV found 1: %+v", len(faces), faces)
	}
	got := faces[0]

	// A pixel of tolerance: the arithmetic is identical, so this is float
	// accumulation rather than a different algorithm.
	const tol = 1.0
	if math.Abs(float64(got.Box.X)-ref.Box.X) > tol ||
		math.Abs(float64(got.Box.Y)-ref.Box.Y) > tol ||
		math.Abs(float64(got.Box.W)-ref.Box.W) > tol ||
		math.Abs(float64(got.Box.H)-ref.Box.H) > tol {
		t.Errorf("box = (%.2f, %.2f, %.2f, %.2f), OpenCV = (%.2f, %.2f, %.2f, %.2f)",
			got.Box.X, got.Box.Y, got.Box.W, got.Box.H,
			ref.Box.X, ref.Box.Y, ref.Box.W, ref.Box.H)
	}
	if math.Abs(float64(got.Score)-ref.Score) > 0.01 {
		t.Errorf("score = %.5f, OpenCV = %.5f", got.Score, ref.Score)
	}

	// The landmarks are what a face crop is aligned by, so an eye in the wrong
	// place degrades every embedding rather than erroring.
	for i, want := range ref.Landmarks {
		if math.Abs(float64(got.Landmarks[i][0])-want[0]) > 2 ||
			math.Abs(float64(got.Landmarks[i][1])-want[1]) > 2 {
			t.Errorf("landmark %d = (%.1f, %.1f), OpenCV = (%.1f, %.1f)",
				i, got.Landmarks[i][0], got.Landmarks[i][1], want[0], want[1])
		}
	}
}

// The channel order is the subtlest of the three conventions: feeding RGB to a
// BGR model still detects the face, just in the wrong place with the wrong
// score. Asserted so it cannot silently regress.
func TestFaceDetectorNeedsBGR(t *testing.T) {
	requireONNX(t)

	model, path := yunetModel(t)
	ref := loadYuNetReference(t)

	if model.Channels != assets.ChannelBGR {
		t.Fatalf("the catalog declares %s; YuNet needs BGR", model.Channels)
	}

	detector, err := NewFaceDetector(model, path, onnx.DefaultSessionOptions())
	if err != nil {
		t.Fatal(err)
	}
	defer detector.Close()
	detector.ScoreThreshold = ref.ScoreThreshold

	frame := loadFixtureFrame(t, model.InputSize)

	// Force RGB and confirm the answer visibly moves. If this ever stops
	// differing, the swap has stopped happening.
	detector.prep.BGR = false
	wrong, err := detector.Detect(frame, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(wrong) == 0 {
		t.Skip("RGB found nothing at all; the comparison needs a detection")
	}

	drift := math.Abs(float64(wrong[0].Box.Y) - ref.Box.Y)
	if drift < 1 {
		t.Errorf("RGB and BGR gave the same box (drift %.2f px); the channel swap is not being applied", drift)
	}
	t.Logf("RGB drifts %.1f px in y and %.4f in score - plausible enough to miss, wrong enough to matter",
		drift, math.Abs(float64(wrong[0].Score)-ref.Score))
}

// Decoding one stride finds only faces in that size band, so the catalog must
// declare all twelve outputs. A model entry with three would load and quietly
// miss most faces.
func TestYuNetDeclaresEveryStride(t *testing.T) {
	model, ok := assets.FindModel("face_detection_yunet_2023mar.onnx")
	if !ok {
		t.Fatal("YuNet is not in the catalog")
	}

	if want := len(YuNetStrides) * 4; len(model.Outputs) != want {
		t.Fatalf("catalog declares %d outputs, the artifact has %d", len(model.Outputs), want)
	}
	if model.InputSize != 640 {
		t.Errorf("InputSize = %d; the artifact is fixed at 640", model.InputSize)
	}
	if model.Pixels != assets.PixelRaw {
		t.Errorf("Pixels = %s; YuNet takes 0-255", model.Pixels)
	}

	// A model whose declared outputs cannot be YuNet must be refused rather
	// than loaded and mis-indexed.
	bad := model
	bad.Outputs = []string{"cls_8", "obj_8", "bbox_8"}
	if _, err := NewFaceDetector(bad, "unused.onnx", onnx.SessionOptions{}); err == nil {
		t.Error("a three-output model was accepted as YuNet")
	}
}
