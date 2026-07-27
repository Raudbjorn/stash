package native

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
)

// Training a tagging head from the user's own markers.
//
// This is the plan's Tier C, and the honest answer to zero-shot's ceiling: no
// open model reproduces fine-grained action labels, and prompting will not
// reliably separate one from another. But a linear head over cached embeddings,
// fitted against markers the user already placed, learns THEIR taxonomy - and
// trains in seconds on a CPU because the expensive part (the embeddings) is
// already done.
//
// What ships is a trainer, not weights. Nothing is distributed, nothing is
// licensed, and the result is specific to one library.

// ErrNotEnoughData reports a training set too small to fit anything.
var ErrNotEnoughData = errors.New("not enough labelled examples to train a head")

// Sample is one labelled embedding.
type Sample struct {
	// Vector is the frame's embedding, already normalised.
	Vector []float32
	// Labels are the label indices that are positive for this frame.
	Labels []int
}

// TrainOptions configure the fit.
type TrainOptions struct {
	// Epochs is how many passes over the data.
	Epochs int
	// LearningRate is the step size.
	LearningRate float32
	// L2 is the weight decay, which is what stops a head with 768 inputs and a
	// few hundred examples from memorising them.
	L2 float32
	// BatchSize is the minibatch size.
	BatchSize int
	// ValidationFraction is held out to measure the result. Without it the
	// reported metrics would describe the training set, which always looks
	// good and means nothing.
	ValidationFraction float64
	// Seed makes the shuffle and the split reproducible, so two runs on the
	// same data give the same head.
	Seed int64
	// MinPositives is the fewest examples a label needs to be worth fitting.
	// A label with three examples produces a classifier that fires on noise.
	MinPositives int
}

// DefaultTrainOptions are tuned for the expected shape of this problem: a few
// hundred to a few thousand frames, 768 inputs, a few dozen labels.
func DefaultTrainOptions() TrainOptions {
	return TrainOptions{
		Epochs:             60,
		LearningRate:       0.5,
		L2:                 1e-4,
		BatchSize:          32,
		ValidationFraction: 0.2,
		Seed:               1,
		MinPositives:       10,
	}
}

// TrainMetrics describe the fitted head's quality.
//
// Reported per label rather than only in aggregate, because the aggregate hides
// exactly what matters: a head that is excellent on three common labels and
// useless on twenty rare ones has a fine average.
type TrainMetrics struct {
	Samples         int                     `json:"samples"`
	TrainingSamples int                     `json:"training_samples"`
	ValidationCount int                     `json:"validation_samples"`
	Epochs          int                     `json:"epochs"`
	FinalLoss       float64                 `json:"final_loss"`
	PerLabel        map[string]LabelMetrics `json:"per_label"`
	MacroF1         float64                 `json:"macro_f1"`
	SkippedLabels   []string                `json:"skipped_labels,omitempty"`
}

// LabelMetrics is one label's validation performance.
type LabelMetrics struct {
	Positives int     `json:"positives"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
	F1        float64 `json:"f1"`
}

// Train fits a multi-label logistic head.
//
// Plain minibatch gradient descent rather than anything cleverer: the problem
// is convex, the data is small, and this trains in seconds. A more sophisticated
// optimiser would add dependencies and tuning knobs to reach the same optimum.
func Train(ctx context.Context, labels []string, dim int, samples []Sample, opts TrainOptions) (*Head, *TrainMetrics, error) {
	if dim <= 0 {
		return nil, nil, fmt.Errorf("embedding dimension must be positive")
	}
	if len(labels) == 0 {
		return nil, nil, fmt.Errorf("no labels to train")
	}
	if len(samples) < 2 {
		return nil, nil, ErrNotEnoughData
	}

	applyTrainDefaults(&opts)

	// Labels with too few positives are dropped rather than fitted. A head
	// trained on three examples of a label does not learn it, it memorises
	// them, and then fires on anything that rhymes.
	positives := make([]int, len(labels))
	for _, sample := range samples {
		for _, index := range sample.Labels {
			if index >= 0 && index < len(labels) {
				positives[index]++
			}
		}
	}

	var (
		kept        []string
		keptIndexes []int
		skipped     []string
	)
	for i, label := range labels {
		if positives[i] >= opts.MinPositives {
			kept = append(kept, label)
			keptIndexes = append(keptIndexes, i)
			continue
		}
		skipped = append(skipped, label)
	}
	if len(kept) == 0 {
		return nil, nil, fmt.Errorf("%w: no label has at least %d examples",
			ErrNotEnoughData, opts.MinPositives)
	}

	// Remap so the weight rows are dense over the labels actually being fitted.
	remap := make(map[int]int, len(keptIndexes))
	for newIndex, oldIndex := range keptIndexes {
		remap[oldIndex] = newIndex
	}

	dense := make([]Sample, 0, len(samples))
	for _, sample := range samples {
		if len(sample.Vector) != dim {
			return nil, nil, fmt.Errorf("a sample vector is %d wide, expected %d", len(sample.Vector), dim)
		}
		var mapped []int
		for _, index := range sample.Labels {
			if newIndex, ok := remap[index]; ok {
				mapped = append(mapped, newIndex)
			}
		}
		dense = append(dense, Sample{Vector: sample.Vector, Labels: mapped})
	}

	rng := rand.New(rand.NewSource(opts.Seed))

	// Shuffled before splitting: samples arrive scene by scene, so an unshuffled
	// tail would be one scene's frames and the validation score would measure
	// how well the head does on that scene rather than in general.
	order := rng.Perm(len(dense))
	shuffled := make([]Sample, len(dense))
	for i, index := range order {
		shuffled[i] = dense[index]
	}

	validationCount := int(float64(len(shuffled)) * opts.ValidationFraction)
	if validationCount >= len(shuffled) {
		validationCount = len(shuffled) / 2
	}
	training := shuffled[validationCount:]
	validation := shuffled[:validationCount]

	if len(training) == 0 {
		return nil, nil, ErrNotEnoughData
	}

	stride := dim + 1
	weights := make([]float32, len(kept)*stride)

	var finalLoss float64
	for epoch := 0; epoch < opts.Epochs; epoch++ {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		// Reshuffled each epoch so the minibatches differ; with a fixed order
		// the gradient noise is identical every pass and adds nothing.
		rng.Shuffle(len(training), func(i, j int) {
			training[i], training[j] = training[j], training[i]
		})

		finalLoss = runEpoch(training, weights, kept, dim, opts)
	}

	head := &Head{
		Labels:      kept,
		Dim:         dim,
		Weights:     weights,
		Sigmoid:     true,
		Temperature: 1,
	}

	metrics := &TrainMetrics{
		Samples:         len(samples),
		TrainingSamples: len(training),
		ValidationCount: len(validation),
		Epochs:          opts.Epochs,
		FinalLoss:       finalLoss,
		SkippedLabels:   skipped,
		PerLabel:        map[string]LabelMetrics{},
	}
	sort.Strings(metrics.SkippedLabels)

	evaluate(head, validation, metrics)
	return head, metrics, nil
}

func applyTrainDefaults(opts *TrainOptions) {
	defaults := DefaultTrainOptions()
	if opts.Epochs <= 0 {
		opts.Epochs = defaults.Epochs
	}
	if opts.LearningRate <= 0 {
		opts.LearningRate = defaults.LearningRate
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaults.BatchSize
	}
	if opts.ValidationFraction < 0 || opts.ValidationFraction >= 1 {
		opts.ValidationFraction = defaults.ValidationFraction
	}
	if opts.MinPositives <= 0 {
		opts.MinPositives = defaults.MinPositives
	}
	if opts.Seed == 0 {
		opts.Seed = defaults.Seed
	}
}

// runEpoch performs one pass, returning the mean loss.
func runEpoch(training []Sample, weights []float32, labels []string, dim int, opts TrainOptions) float64 {
	stride := dim + 1
	gradient := make([]float32, len(weights))
	targets := make([]float32, len(labels))

	var (
		totalLoss float64
		counted   int
	)

	for start := 0; start < len(training); start += opts.BatchSize {
		end := start + opts.BatchSize
		if end > len(training) {
			end = len(training)
		}
		batch := training[start:end]

		for i := range gradient {
			gradient[i] = 0
		}

		for _, sample := range batch {
			for i := range targets {
				targets[i] = 0
			}
			for _, index := range sample.Labels {
				targets[index] = 1
			}

			for label := 0; label < len(labels); label++ {
				row := weights[label*stride : (label+1)*stride]

				var logit float32
				for j := 0; j < dim; j++ {
					logit += row[j] * sample.Vector[j]
				}
				logit += row[dim]

				predicted := sigmoid(logit)
				diff := predicted - targets[label]

				grad := gradient[label*stride : (label+1)*stride]
				for j := 0; j < dim; j++ {
					grad[j] += diff * sample.Vector[j]
				}
				grad[dim] += diff

				totalLoss += binaryCrossEntropy(predicted, targets[label])
				counted++
			}
		}

		scale := opts.LearningRate / float32(len(batch))
		for label := 0; label < len(labels); label++ {
			row := weights[label*stride : (label+1)*stride]
			grad := gradient[label*stride : (label+1)*stride]

			for j := 0; j < dim; j++ {
				// L2 on the weights but NOT the bias: penalising the bias would
				// pull every prediction toward 0.5 regardless of how common the
				// label is, which is exactly wrong for rare labels.
				row[j] -= scale*grad[j] + opts.L2*row[j]
			}
			row[dim] -= scale * grad[dim]
		}
	}

	if counted == 0 {
		return 0
	}
	return totalLoss / float64(counted)
}

func binaryCrossEntropy(predicted, target float32) float64 {
	// Clamped away from the asymptotes: log(0) is -Inf and would make the
	// reported loss NaN from the first saturated prediction onward.
	const eps = 1e-7
	p := float64(predicted)
	if p < eps {
		p = eps
	}
	if p > 1-eps {
		p = 1 - eps
	}
	if target > 0.5 {
		return -math.Log(p)
	}
	return -math.Log(1 - p)
}

// evaluate measures the head against held-out samples.
func evaluate(head *Head, validation []Sample, metrics *TrainMetrics) {
	if len(validation) == 0 {
		return
	}

	truePositives := make([]int, len(head.Labels))
	falsePositives := make([]int, len(head.Labels))
	falseNegatives := make([]int, len(head.Labels))
	positives := make([]int, len(head.Labels))

	scores := make([]float32, len(head.Labels))
	actual := make([]bool, len(head.Labels))

	for _, sample := range validation {
		for i := range actual {
			actual[i] = false
		}
		for _, index := range sample.Labels {
			if index >= 0 && index < len(actual) {
				actual[index] = true
				positives[index]++
			}
		}

		if err := head.Score(sample.Vector, scores); err != nil {
			continue
		}

		for i := range head.Labels {
			predicted := scores[i] >= 0.5
			switch {
			case predicted && actual[i]:
				truePositives[i]++
			case predicted && !actual[i]:
				falsePositives[i]++
			case !predicted && actual[i]:
				falseNegatives[i]++
			}
		}
	}

	var (
		f1Total float64
		scored  int
	)
	for i, label := range head.Labels {
		precision := ratio(truePositives[i], truePositives[i]+falsePositives[i])
		recall := ratio(truePositives[i], truePositives[i]+falseNegatives[i])

		var f1 float64
		if precision+recall > 0 {
			f1 = 2 * precision * recall / (precision + recall)
		}

		metrics.PerLabel[label] = LabelMetrics{
			Positives: positives[i],
			Precision: precision,
			Recall:    recall,
			F1:        f1,
		}

		// Only labels that actually appear in the validation split count
		// toward the macro average; a label with no held-out examples would
		// otherwise contribute a free zero.
		if positives[i] > 0 {
			f1Total += f1
			scored++
		}
	}

	if scored > 0 {
		metrics.MacroF1 = f1Total / float64(scored)
	}
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}
