package native

import (
	"fmt"
	"sync"

	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/onnx"
)

// Running YuNet.
//
// The decode in faces.go is the arithmetic; this is the part that knows the
// artifact's actual shape, which was verified against the real file rather than
// assumed:
//
//   - The input is 640x640 with a FIXED batch of one. Not 320, which an earlier
//     draft of the catalog guessed.
//   - There are TWELVE outputs, not three: classification, objectness, box and
//     landmarks at each of strides 8, 16 and 32. A single stride only finds the
//     faces that fall in its size band, so decoding one would silently miss
//     most of them.
//   - The input is BGR at 0-255. Feeding RGB moves boxes by around twelve
//     pixels and scores by under 0.01 - close enough to look right.
//
// All three are enforced by a differential test against OpenCV's own
// FaceDetectorYN on a committed fixture.

// YuNetStrides are the feature-map strides the model predicts at.
var YuNetStrides = []int{8, 16, 32}

// FaceDetector finds faces and their landmarks in a frame.
type FaceDetector struct {
	model   assets.Model
	session *onnx.Session
	prep    *Preprocessor

	// ScoreThreshold is the minimum geometric-mean score to report.
	ScoreThreshold float32
	// IOUThreshold is the overlap above which two boxes are the same face.
	IOUThreshold float32

	mu sync.Mutex
}

// NewFaceDetector loads a YuNet model.
func NewFaceDetector(model assets.Model, modelPath string, opts onnx.SessionOptions) (*FaceDetector, error) {
	if len(model.Outputs) != len(YuNetStrides)*4 {
		return nil, fmt.Errorf(
			"model %s declares %d outputs; YuNet emits %d (four tensors at each of %d strides)",
			model.Name, len(model.Outputs), len(YuNetStrides)*4, len(YuNetStrides))
	}

	session, err := onnx.NewSession(model.Name, modelPath, model.Inputs, model.Outputs, opts)
	if err != nil {
		return nil, err
	}

	return &FaceDetector{
		model:          model,
		session:        session,
		prep:           NewPreprocessor(model),
		ScoreThreshold: 0.6,
		IOUThreshold:   0.3,
	}, nil
}

// InputSize is the square side the detector expects.
func (d *FaceDetector) InputSize() int { return d.model.InputSize }

// Close releases the session.
func (d *FaceDetector) Close() error { return d.session.Close() }

// outputShapes returns the shapes the twelve outputs take for this input size.
//
// Derived rather than hard-coded so a re-exported model at a different
// resolution still works: at stride s a 640-wide input has (640/s)^2 cells,
// each carrying one class score, one objectness, four box values and ten
// landmark values.
func (d *FaceDetector) outputShapes() [][]int64 {
	size := int64(d.model.InputSize)
	shapes := make([][]int64, 0, len(d.model.Outputs))

	// The order must match model.Outputs: all cls, then all obj, then bbox,
	// then kps - which is the order the artifact declares them in.
	for _, width := range []int64{1, 1, 4, 10} {
		for _, stride := range YuNetStrides {
			cells := (size / int64(stride)) * (size / int64(stride))
			shapes = append(shapes, []int64{1, cells, width})
		}
	}
	return shapes
}

// Detect finds faces in one frame.
//
// The frame must already be InputSize square; sourceW and sourceH are the
// dimensions it was resized FROM, so the returned boxes are in the original
// frame's coordinates. Skipping that scaling produces plausibly-shaped boxes in
// the wrong place, which nothing downstream would catch.
func (d *FaceDetector) Detect(frame *FrameData, sourceW, sourceH int) ([]FaceDetection, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	batch := d.prep.NewBatch(1)
	if err := d.prep.Add(batch, frame); err != nil {
		return nil, err
	}

	outputs, err := d.session.Run([]*onnx.Tensor{d.prep.TrimTensor(batch)}, d.outputShapes())
	if err != nil {
		return nil, err
	}

	// Indexed by position within each group of three, matching outputShapes.
	byStride := func(group, i int) []float32 { return outputs[group*len(YuNetStrides)+i].Data }

	var all []FaceDetection
	for i, stride := range YuNetStrides {
		decoded, err := DecodeYuNet(
			byStride(0, i), // cls
			byStride(1, i), // obj
			byStride(2, i), // bbox
			byStride(3, i), // kps
			stride, d.model.InputSize, d.model.InputSize, d.ScoreThreshold,
		)
		if err != nil {
			return nil, err
		}
		all = append(all, decoded...)
	}

	kept := NonMaxSuppression(all, d.IOUThreshold)
	if sourceW > 0 && sourceH > 0 {
		kept = ScaleDetections(kept, d.model.InputSize, d.model.InputSize, sourceW, sourceH)
	}
	return kept, nil
}
