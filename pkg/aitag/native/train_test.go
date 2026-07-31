package native

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"testing"
)

// The trainer is tested on synthetic data with a KNOWN answer, so "it learned
// something" is checkable rather than asserted. Real embeddings would only tell
// us the loss went down.

// makeSeparableData builds vectors that are linearly separable by construction:
// each label owns a direction, and a positive sample points along it plus noise.
func makeSeparableData(t *testing.T, dim, labels, perLabel int, noise float64, seed int64) []Sample {
	t.Helper()

	rng := rand.New(rand.NewSource(seed))

	directions := make([][]float32, labels)
	for i := range directions {
		v := make([]float32, dim)
		v[i%dim] = 1
		directions[i] = v
	}

	var samples []Sample
	for label := 0; label < labels; label++ {
		for n := 0; n < perLabel; n++ {
			vector := make([]float32, dim)
			copy(vector, directions[label])
			for j := range vector {
				vector[j] += float32(rng.NormFloat64() * noise)
			}
			NormalizeInPlace(vector, dim)
			samples = append(samples, Sample{Vector: vector, Labels: []int{label}})
		}
	}

	// Negatives with no label at all, so the head has something to say no to.
	for n := 0; n < perLabel*labels/2; n++ {
		vector := make([]float32, dim)
		for j := range vector {
			vector[j] = float32(rng.NormFloat64() * 0.1)
		}
		NormalizeInPlace(vector, dim)
		samples = append(samples, Sample{Vector: vector})
	}

	return samples
}

func TestTrainLearnsSeparableLabels(t *testing.T) {
	labels := []string{"a", "b", "c"}
	samples := makeSeparableData(t, 16, len(labels), 60, 0.15, 42)

	head, metrics, err := Train(context.Background(), labels, 16, samples, DefaultTrainOptions())
	if err != nil {
		t.Fatalf("Train: %v", err)
	}

	if len(head.Labels) != 3 {
		t.Fatalf("fitted %v, want all three labels", head.Labels)
	}
	if !head.Sigmoid {
		t.Error("a trained head must use sigmoid, not raw similarity")
	}

	// Separable data with this much signal should be learned well. A low score
	// here means the optimiser is not working, not that the problem is hard.
	if metrics.MacroF1 < 0.85 {
		t.Errorf("macro F1 = %.3f on cleanly separable data; the fit is not working", metrics.MacroF1)
	}
	if metrics.FinalLoss > 0.5 {
		t.Errorf("final loss = %.3f, expected it to converge", metrics.FinalLoss)
	}

	// And it must generalise: a fresh vector along a label's direction should
	// fire for that label and not the others.
	probe := make([]float32, 16)
	probe[1] = 1
	scores := make([]float32, len(head.Labels))
	if err := head.Score(probe, scores); err != nil {
		t.Fatal(err)
	}
	if scores[1] < 0.5 {
		t.Errorf("label b scored %.3f on its own direction", scores[1])
	}
	if scores[0] > 0.5 || scores[2] > 0.5 {
		t.Errorf("other labels fired on b's direction: %v", scores)
	}
}

// A label with a handful of examples produces a classifier that memorises them
// and then fires on anything similar. Dropping it is more honest than shipping
// it, and the report says which were dropped.
func TestRareLabelsAreSkipped(t *testing.T) {
	labels := []string{"common", "rare"}

	samples := makeSeparableData(t, 8, 1, 50, 0.1, 7)
	// Three examples of the second label: far below the threshold.
	for i := 0; i < 3; i++ {
		vector := make([]float32, 8)
		vector[1] = 1
		NormalizeInPlace(vector, 8)
		samples = append(samples, Sample{Vector: vector, Labels: []int{1}})
	}

	head, metrics, err := Train(context.Background(), labels, 8, samples, DefaultTrainOptions())
	if err != nil {
		t.Fatalf("Train: %v", err)
	}

	if len(head.Labels) != 1 || head.Labels[0] != "common" {
		t.Errorf("fitted %v, want only the common label", head.Labels)
	}
	if len(metrics.SkippedLabels) != 1 || metrics.SkippedLabels[0] != "rare" {
		t.Errorf("skipped = %v, want [rare]", metrics.SkippedLabels)
	}
}

// Metrics must come from HELD-OUT data. Reporting training-set performance
// always looks good and tells the user nothing about whether to trust the head.
func TestMetricsUseHeldOutData(t *testing.T) {
	labels := []string{"a"}
	samples := makeSeparableData(t, 8, 1, 100, 0.1, 3)

	_, metrics, err := Train(context.Background(), labels, 8, samples, DefaultTrainOptions())
	if err != nil {
		t.Fatal(err)
	}

	if metrics.ValidationCount == 0 {
		t.Fatal("nothing was held out; the metrics describe the training set")
	}
	if metrics.TrainingSamples+metrics.ValidationCount != len(samples) {
		t.Errorf("the split does not account for every sample: %d + %d != %d",
			metrics.TrainingSamples, metrics.ValidationCount, len(samples))
	}
	// Roughly the requested fraction.
	fraction := float64(metrics.ValidationCount) / float64(len(samples))
	if math.Abs(fraction-0.2) > 0.05 {
		t.Errorf("held out %.2f, want about 0.20", fraction)
	}
}

// Two runs on the same data must give the same head, or a user comparing
// before and after a change cannot tell what caused the difference.
func TestTrainingIsReproducible(t *testing.T) {
	labels := []string{"a", "b"}
	samples := makeSeparableData(t, 8, 2, 40, 0.2, 11)

	first, firstMetrics, err := Train(context.Background(), labels, 8, samples, DefaultTrainOptions())
	if err != nil {
		t.Fatal(err)
	}
	second, secondMetrics, err := Train(context.Background(), labels, 8, samples, DefaultTrainOptions())
	if err != nil {
		t.Fatal(err)
	}

	if len(first.Weights) != len(second.Weights) {
		t.Fatal("weight blocks differ in size")
	}
	for i := range first.Weights {
		if first.Weights[i] != second.Weights[i] {
			t.Fatalf("weight %d differs between runs: %v vs %v", i, first.Weights[i], second.Weights[i])
		}
	}
	if firstMetrics.MacroF1 != secondMetrics.MacroF1 {
		t.Errorf("metrics differ between runs: %v vs %v", firstMetrics.MacroF1, secondMetrics.MacroF1)
	}
}

// A different seed must actually produce a different split, or the seed is
// decorative.
func TestSeedChangesTheSplit(t *testing.T) {
	labels := []string{"a", "b"}
	samples := makeSeparableData(t, 8, 2, 40, 0.3, 5)

	opts := DefaultTrainOptions()
	first, _, err := Train(context.Background(), labels, 8, samples, opts)
	if err != nil {
		t.Fatal(err)
	}

	opts.Seed = 999
	second, _, err := Train(context.Background(), labels, 8, samples, opts)
	if err != nil {
		t.Fatal(err)
	}

	identical := true
	for i := range first.Weights {
		if first.Weights[i] != second.Weights[i] {
			identical = false
			break
		}
	}
	if identical {
		t.Error("changing the seed produced identical weights")
	}
}

func TestTrainRejectsInsufficientData(t *testing.T) {
	if _, _, err := Train(context.Background(), []string{"a"}, 8, nil, DefaultTrainOptions()); !errors.Is(err, ErrNotEnoughData) {
		t.Errorf("Train with no samples = %v, want ErrNotEnoughData", err)
	}

	// Enough samples, but no label reaches the minimum.
	samples := []Sample{
		{Vector: make([]float32, 8), Labels: []int{0}},
		{Vector: make([]float32, 8)},
	}
	if _, _, err := Train(context.Background(), []string{"a"}, 8, samples, DefaultTrainOptions()); !errors.Is(err, ErrNotEnoughData) {
		t.Errorf("Train with too few positives = %v, want ErrNotEnoughData", err)
	}
}

func TestTrainRejectsMismatchedVectors(t *testing.T) {
	samples := makeSeparableData(t, 8, 1, 20, 0.1, 1)
	samples[5].Vector = make([]float32, 4)

	if _, _, err := Train(context.Background(), []string{"a"}, 8, samples, DefaultTrainOptions()); err == nil {
		t.Error("a sample of the wrong width was accepted")
	}
}

// The loss must not become NaN, however saturated the predictions get: a NaN
// loss makes every reported metric meaningless without failing.
func TestLossStaysFinite(t *testing.T) {
	labels := []string{"a"}
	// Perfectly separable with no noise, which drives predictions to the
	// asymptotes where log(0) would appear.
	var samples []Sample
	for i := 0; i < 60; i++ {
		positive := make([]float32, 4)
		positive[0] = 1
		samples = append(samples, Sample{Vector: positive, Labels: []int{0}})

		negative := make([]float32, 4)
		negative[1] = 1
		samples = append(samples, Sample{Vector: negative})
	}

	opts := DefaultTrainOptions()
	opts.Epochs = 500
	opts.LearningRate = 5

	_, metrics, err := Train(context.Background(), labels, 4, samples, opts)
	if err != nil {
		t.Fatal(err)
	}
	if math.IsNaN(metrics.FinalLoss) || math.IsInf(metrics.FinalLoss, 0) {
		t.Fatalf("final loss = %v", metrics.FinalLoss)
	}
}

func TestTrainHonoursCancellation(t *testing.T) {
	labels := []string{"a"}
	samples := makeSeparableData(t, 8, 1, 50, 0.1, 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	opts := DefaultTrainOptions()
	opts.Epochs = 100000

	if _, _, err := Train(ctx, labels, 8, samples, opts); !errors.Is(err, context.Canceled) {
		t.Errorf("Train = %v, want context.Canceled", err)
	}
}

// Per-label metrics matter more than the average: a head that is excellent on
// three common labels and useless on twenty rare ones has a fine average.
func TestPerLabelMetricsAreReported(t *testing.T) {
	labels := []string{"a", "b"}
	samples := makeSeparableData(t, 8, 2, 60, 0.15, 13)

	_, metrics, err := Train(context.Background(), labels, 8, samples, DefaultTrainOptions())
	if err != nil {
		t.Fatal(err)
	}

	for _, label := range labels {
		m, ok := metrics.PerLabel[label]
		if !ok {
			t.Errorf("no metrics for %q", label)
			continue
		}
		if m.Positives == 0 {
			t.Errorf("%q reports no validation positives", label)
		}
		if m.Precision < 0 || m.Precision > 1 || m.Recall < 0 || m.Recall > 1 {
			t.Errorf("%q has out-of-range metrics: %+v", label, m)
		}
	}
}
