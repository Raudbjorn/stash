package native

import (
	"fmt"
	"math"
	"sort"
)

// Classifier heads over embeddings.
//
// One shape serves both approaches. A zero-shot head's weights are text
// embeddings of label prompts; a trained head's weights are fitted from the
// user's own markers. Downstream code cannot tell them apart, which is what
// lets the trained head replace zero-shot without touching the pipeline.

// Head is a multi-label linear classifier.
type Head struct {
	// Labels names each output, in weight order.
	Labels []string
	// Dim is the input width.
	Dim int
	// Weights is len(Labels) rows of Dim+1 values, bias last.
	Weights []float32
	// Sigmoid selects the activation. A trained logistic head produces
	// independent per-label probabilities; a zero-shot head produces cosine
	// similarities that are already in [-1,1] and are thresholded directly.
	Sigmoid bool
	// Temperature scales logits before activation. For zero-shot this is the
	// contrastive model's learned scale, without which every similarity lands
	// in a narrow band around zero and no threshold separates anything.
	Temperature float32
}

// NewHead builds a head, validating the weight block.
func NewHead(labels []string, dim int, weights []float32) (*Head, error) {
	if dim <= 0 {
		return nil, fmt.Errorf("head dimension must be positive")
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("head has no labels")
	}
	if len(weights) != len(labels)*(dim+1) {
		return nil, fmt.Errorf("head has %d weights, expected %d for %d labels of dimension %d",
			len(weights), len(labels)*(dim+1), len(labels), dim)
	}
	return &Head{Labels: labels, Dim: dim, Weights: weights, Sigmoid: true, Temperature: 1}, nil
}

// NewZeroShotHead builds a head from label embeddings.
//
// The embeddings are the model's own text-tower output for each label's prompt.
// They are normalised so the dot product is a cosine similarity, and the bias
// is zero because there is nothing to fit.
func NewZeroShotHead(labels []string, dim int, labelEmbeddings []float32, temperature float32) (*Head, error) {
	if len(labelEmbeddings) != len(labels)*dim {
		return nil, fmt.Errorf("expected %d label embedding values, got %d",
			len(labels)*dim, len(labelEmbeddings))
	}

	normalised := make([]float32, len(labelEmbeddings))
	copy(normalised, labelEmbeddings)
	NormalizeInPlace(normalised, dim)

	// Repacked into the weights-plus-bias layout so both head kinds share one
	// evaluation path.
	weights := make([]float32, len(labels)*(dim+1))
	for i := range labels {
		copy(weights[i*(dim+1):], normalised[i*dim:(i+1)*dim])
		weights[i*(dim+1)+dim] = 0
	}

	if temperature <= 0 {
		temperature = 1
	}
	return &Head{
		Labels:      labels,
		Dim:         dim,
		Weights:     weights,
		Sigmoid:     false,
		Temperature: temperature,
	}, nil
}

// Score evaluates the head over one vector, writing len(Labels) values.
//
// The INPUT VECTOR MUST ALREADY BE NORMALISED for a zero-shot head. Only the
// label weights are normalised here, so an unnormalised input silently scales
// every score by its length - scores that look reasonable, ordered correctly,
// and sit below whatever threshold the user picked. Call NormalizeInPlace on
// the embeddings first; the provider does.
//
// The output buffer is supplied so a caller scoring 900 frames does not
// allocate 900 slices.
func (h *Head) Score(vector []float32, out []float32) error {
	if len(vector) != h.Dim {
		return fmt.Errorf("vector is %d wide, head expects %d", len(vector), h.Dim)
	}
	if len(out) < len(h.Labels) {
		return fmt.Errorf("output buffer holds %d, need %d", len(out), len(h.Labels))
	}

	stride := h.Dim + 1
	for i := range h.Labels {
		row := h.Weights[i*stride : (i+1)*stride]

		var sum float32
		for j := 0; j < h.Dim; j++ {
			sum += row[j] * vector[j]
		}
		sum += row[h.Dim]
		sum *= h.Temperature

		if h.Sigmoid {
			out[i] = sigmoid(sum)
		} else {
			out[i] = sum
		}
	}
	return nil
}

func sigmoid(x float32) float32 {
	// Clamped before exponentiating: a large negative logit overflows to +Inf
	// and yields a NaN probability rather than the zero it should.
	if x < -40 {
		return 0
	}
	if x > 40 {
		return 1
	}
	return float32(1 / (1 + math.Exp(-float64(x))))
}

// Detection is one label firing on one frame.
type Detection struct {
	Label      string
	Confidence float32
}

// Apply scores a vector and returns the labels above a threshold.
func (h *Head) Apply(vector []float32, threshold float32, scratch []float32) ([]Detection, []float32, error) {
	if cap(scratch) < len(h.Labels) {
		scratch = make([]float32, len(h.Labels))
	}
	scratch = scratch[:len(h.Labels)]

	if err := h.Score(vector, scratch); err != nil {
		return nil, scratch, err
	}

	var out []Detection
	for i, score := range scratch {
		if score >= threshold {
			out = append(out, Detection{Label: h.Labels[i], Confidence: score})
		}
	}

	// Highest first, so a caller taking the top N gets the most confident.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Confidence > out[j].Confidence })
	return out, scratch, nil
}

// LabelIndex maps a label to its position, for training and evaluation.
func (h *Head) LabelIndex() map[string]int {
	out := make(map[string]int, len(h.Labels))
	for i, label := range h.Labels {
		out[label] = i
	}
	return out
}
