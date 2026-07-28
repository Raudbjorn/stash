package native

import (
	"fmt"

	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/onnx"
)

// Turning decoded frames into a model's input tensor.
//
// The arithmetic is trivial and the conventions are not: channel order, layout
// and normalisation constants are three independent ways to produce embeddings
// that look plausible and are quietly wrong. None of them errors, so each is
// stated explicitly here rather than inherited from whatever a library defaults
// to.

// Preprocessor converts decoded RGB frames into a batched NCHW tensor.
type Preprocessor struct {
	// Size is the square side the model expects.
	Size int
	// Mean and Std normalise each channel, applied after scaling to [0,1].
	// Ignored when Raw is set.
	Mean [3]float32
	Std  [3]float32

	// BGR swaps the channel order on the way in.
	//
	// Not cosmetic. YuNet was trained through OpenCV and genuinely wants BGR;
	// feeding it RGB moves its boxes by around twelve pixels and its scores by
	// under 0.01 - close enough to look correct, far enough to be wrong, and
	// invisible to every assertion downstream.
	BGR bool

	// Raw passes 0-255 through without scaling or normalising, for a graph
	// with the preprocessing baked in. Applying the usual [0,1] scaling to such
	// a model does not error; it just makes it markedly less confident.
	Raw bool
}

// NewPreprocessor builds one from a catalog entry.
func NewPreprocessor(model assets.Model) *Preprocessor {
	std := model.Std
	// A zero standard deviation would divide by zero and fill the tensor with
	// infinities that propagate silently through the whole graph.
	for i := range std {
		if std[i] == 0 {
			std[i] = 1
		}
	}

	return &Preprocessor{
		Size: model.InputSize,
		Mean: model.Mean,
		Std:  std,
		BGR:  model.Channels == assets.ChannelBGR,
		Raw:  model.Pixels == assets.PixelRaw,
	}
}

// Batch is a preprocessed set of frames ready for inference.
type Batch struct {
	// Tensor is NCHW float32.
	Tensor *onnx.Tensor
	// Times are the source frames' timestamps, aligned with the batch rows.
	Times []float64
	// Indexes are the source frames' positions.
	Indexes []int
}

// Size is the number of frames in the batch.
func (b *Batch) Size() int { return len(b.Times) }

// NewBatch allocates a batch of the given capacity.
func (p *Preprocessor) NewBatch(capacity int) *Batch {
	return &Batch{
		Tensor:  onnx.NewTensor(int64(capacity), 3, int64(p.Size), int64(p.Size)),
		Times:   make([]float64, 0, capacity),
		Indexes: make([]int, 0, capacity),
	}
}

// Reset empties a batch for reuse without reallocating its tensor.
func (b *Batch) Reset() {
	b.Times = b.Times[:0]
	b.Indexes = b.Indexes[:0]
}

// Add writes one frame into the batch.
//
// The frame must already be Size x Size: resizing happens in ffmpeg, in the
// same pass that decodes, rather than here where it would be both slower and a
// second place for the aspect-ratio convention to drift.
func (p *Preprocessor) Add(batch *Batch, frame *FrameData) error {
	expected := p.Size * p.Size * 3
	if len(frame.RGB) != expected {
		return fmt.Errorf("frame is %d bytes, expected %d for a %dx%d RGB image",
			len(frame.RGB), expected, p.Size, p.Size)
	}

	row := batch.Size()
	plane := p.Size * p.Size
	if (row+1)*3*plane > len(batch.Tensor.Data) {
		return fmt.Errorf("batch is full")
	}

	base := row * 3 * plane
	data := batch.Tensor.Data

	// The plane order the model wants. Chosen once outside the loop rather
	// than branched per pixel.
	first, third := 0, 2
	if p.BGR {
		first, third = 2, 0
	}

	// NCHW: the three channels are separate planes, not interleaved. The input
	// is interleaved RGB, so this is a transpose as well as a normalisation -
	// writing it interleaved produces a tensor of the right SHAPE holding the
	// wrong values, which no framework will complain about.
	if p.Raw {
		for i := 0; i < plane; i++ {
			data[base+first*plane+i] = float32(frame.RGB[i*3+0])
			data[base+1*plane+i] = float32(frame.RGB[i*3+1])
			data[base+third*plane+i] = float32(frame.RGB[i*3+2])
		}
	} else {
		for i := 0; i < plane; i++ {
			r := float32(frame.RGB[i*3+0]) / 255
			g := float32(frame.RGB[i*3+1]) / 255
			b := float32(frame.RGB[i*3+2]) / 255

			data[base+first*plane+i] = (r - p.Mean[0]) / p.Std[0]
			data[base+1*plane+i] = (g - p.Mean[1]) / p.Std[1]
			data[base+third*plane+i] = (b - p.Mean[2]) / p.Std[2]
		}
	}

	batch.Times = append(batch.Times, frame.Time)
	batch.Indexes = append(batch.Indexes, frame.Index)
	return nil
}

// InputShape returns the tensor shape for the batch's current occupancy.
//
// A batch is allocated at full capacity and the last one is usually short; the
// model must be told the real row count or it reads uninitialised rows and
// returns embeddings for frames that do not exist.
func (p *Preprocessor) InputShape(batch *Batch) []int64 {
	return []int64{int64(batch.Size()), 3, int64(p.Size), int64(p.Size)}
}

// TrimTensor returns a tensor covering only the batch's occupied rows.
func (p *Preprocessor) TrimTensor(batch *Batch) *onnx.Tensor {
	rows := batch.Size()
	plane := p.Size * p.Size * 3
	return &onnx.Tensor{
		Shape: p.InputShape(batch),
		Data:  batch.Tensor.Data[:rows*plane],
	}
}

// BatchSizeFor chooses a batch size from a memory budget.
//
// Bounded by memory rather than fixed because the input tensor is the dominant
// allocation: 224x224x3 float32 is 600 KB per frame, so a batch of 64 is 38 MB
// before the model's own activations. A machine with a small budget should run
// smaller batches rather than fail.
func BatchSizeFor(budgetBytes int64, size int) int {
	if size <= 0 {
		return 1
	}

	perFrame := int64(size) * int64(size) * 3 * 4
	// Activations roughly triple the footprint of the input for a ViT of this
	// shape; the factor is a rule of thumb, and being wrong costs throughput
	// rather than correctness.
	perFrame *= 3

	if budgetBytes <= 0 {
		budgetBytes = 512 << 20
	}

	n := int(budgetBytes / perFrame)
	switch {
	case n < 1:
		return 1
	case n > 64:
		// Beyond this the gains flatten and the latency of a single batch
		// starts to make cancellation feel unresponsive.
		return 64
	default:
		return n
	}
}
