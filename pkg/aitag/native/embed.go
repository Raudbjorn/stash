package native

import (
	"context"
	"fmt"
	"math"
	"sync"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/onnx"
)

// Embedding: frames in, vectors out.
//
// This is the expensive part of the pipeline and the reusable one. Everything
// above it - zero-shot labels, a trained head, similarity search - reads the
// same vectors, so they are computed once and cached rather than recomputed per
// feature.

// Embedder turns frames into vectors using one ONNX model.
type Embedder struct {
	model   assets.Model
	session *onnx.Session
	prep    *Preprocessor

	// mu serialises inference. ONNX sessions are thread-safe for Run, but
	// running several batches at once on the same machine just makes them
	// contend for the same cores - the parallelism is already inside the
	// kernels.
	mu sync.Mutex
}

// NewEmbedder loads an embedding model.
func NewEmbedder(model assets.Model, modelPath string, opts onnx.SessionOptions) (*Embedder, error) {
	if model.Dim <= 0 {
		return nil, fmt.Errorf("model %s does not declare an embedding dimension", model.Name)
	}

	session, err := onnx.NewSession(model.Name, modelPath, model.Inputs, model.Outputs, opts)
	if err != nil {
		return nil, err
	}

	return &Embedder{model: model, session: session, prep: NewPreprocessor(model)}, nil
}

// Name identifies the embedding model. Vectors from different models are not
// comparable, so this is stored alongside them.
func (e *Embedder) Name() string { return e.model.Name }

// Dim is the vector width.
func (e *Embedder) Dim() int { return e.model.Dim }

// InputSize is the frame size the model expects.
func (e *Embedder) InputSize() int { return e.model.InputSize }

// Resample is the interpolation the model was trained with, for the extractor.
func (e *Embedder) Resample() string { return e.model.Resample }

// Close releases the session.
func (e *Embedder) Close() error { return e.session.Close() }

// EmbedBatch runs one batch, appending vectors to out.
func (e *Embedder) EmbedBatch(batch *Batch, out []float32) ([]float32, error) {
	rows := batch.Size()
	if rows == 0 {
		return out, nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	input := e.prep.TrimTensor(batch)
	outputs, err := e.session.Run(
		[]*onnx.Tensor{input},
		[][]int64{{int64(rows), int64(e.model.Dim)}},
	)
	if err != nil {
		return out, err
	}

	return append(out, outputs[0].Data...), nil
}

// EmbedFrames runs the embedder over a whole frame source.
//
// Progress is reported per batch rather than per frame: a frame takes tens of
// milliseconds and a report per frame would be noise.
func (e *Embedder) EmbedFrames(ctx context.Context, frames *FrameSource, batchSize int, sink aitag.Sink, totalFrames int) (*aitag.Embeddings, error) {
	if batchSize <= 0 {
		batchSize = 8
	}

	batch := e.prep.NewBatch(batchSize)
	result := &aitag.Embeddings{Dim: e.model.Dim, Model: e.model.Name}

	var frameBuf []byte

	flush := func() error {
		if batch.Size() == 0 {
			return nil
		}
		data, err := e.EmbedBatch(batch, result.Data)
		if err != nil {
			return err
		}
		result.Data = data
		result.Times = append(result.Times, batch.Times...)
		batch.Reset()
		return nil
	}

	for {
		// Checked between frames rather than only between batches: a cancelled
		// analysis should stop within one frame, not one batch.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		frame, err := frames.Next(frameBuf)
		if err != nil {
			return nil, err
		}
		if frame == nil {
			break
		}
		frameBuf = frame.RGB

		if err := e.prep.Add(batch, frame); err != nil {
			return nil, err
		}

		if batch.Size() >= batchSize {
			if err := flush(); err != nil {
				return nil, err
			}
			reportProgress(sink, len(result.Times), totalFrames)
		}
	}

	if err := flush(); err != nil {
		return nil, err
	}
	if len(result.Times) == 0 {
		return nil, ErrNoFrames
	}

	reportProgress(sink, len(result.Times), totalFrames)
	return result, nil
}

func reportProgress(sink aitag.Sink, done, total int) {
	fraction := -1.0
	if total > 0 {
		fraction = float64(done) / float64(total)
		if fraction > 1 {
			fraction = 1
		}
	}
	sink.Report(aitag.Progress{
		Fraction: fraction,
		Frames:   done,
		Message:  fmt.Sprintf("Analysed %d frames.", done),
	})
}

// NormalizeInPlace scales each vector to unit length.
//
// Required before cosine similarity, and applied in place because the vectors
// are large and the unnormalised form has no other use.
func NormalizeInPlace(vectors []float32, dim int) {
	if dim <= 0 {
		return
	}
	for start := 0; start+dim <= len(vectors); start += dim {
		row := vectors[start : start+dim]

		var sum float64
		for _, v := range row {
			sum += float64(v) * float64(v)
		}
		norm := math.Sqrt(sum)
		// A zero vector cannot be normalised; leaving it alone is better than
		// producing NaNs that spread through every later comparison.
		if norm == 0 {
			continue
		}
		scale := float32(1 / norm)
		for i := range row {
			row[i] *= scale
		}
	}
}

// CosineSimilarity of two equal-length vectors.
//
// Assumes both are already normalised, which is what NormalizeInPlace is for:
// normalising inside the loop would dominate the cost of a similarity search
// over a whole library.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}
