package native

import (
	"math"
	"testing"
)

func TestZeroShotHeadIsCosineSimilarity(t *testing.T) {
	// Two orthogonal label directions, deliberately unnormalised on input so
	// the head's own normalisation is what makes the dot product a cosine.
	labels := []string{"cat", "dog"}
	embeddings := []float32{
		3, 0, 0, // cat, length 3
		0, 5, 0, // dog, length 5
	}

	head, err := NewZeroShotHead(labels, 3, embeddings, 1)
	if err != nil {
		t.Fatalf("NewZeroShotHead: %v", err)
	}

	scores := make([]float32, 2)
	if err := head.Score([]float32{1, 0, 0}, scores); err != nil {
		t.Fatal(err)
	}

	if math.Abs(float64(scores[0]-1)) > 1e-6 {
		t.Errorf("a vector aligned with the cat direction scored %v, want 1", scores[0])
	}
	if math.Abs(float64(scores[1])) > 1e-6 {
		t.Errorf("an orthogonal direction scored %v, want 0", scores[1])
	}
}

// Without a temperature every similarity lands in a narrow band around zero and
// no threshold separates anything; the scale is what makes the head usable.
func TestTemperatureScalesLogits(t *testing.T) {
	head, err := NewZeroShotHead([]string{"x"}, 2, []float32{1, 0}, 10)
	if err != nil {
		t.Fatal(err)
	}

	scores := make([]float32, 1)
	if err := head.Score([]float32{1, 0}, scores); err != nil {
		t.Fatal(err)
	}
	// cos = 1 for a parallel unit vector, times the temperature.
	if math.Abs(float64(scores[0]-10)) > 1e-5 {
		t.Errorf("score = %v, want 10", scores[0])
	}
}

// A zero-shot head normalises its WEIGHTS, not its input. An unnormalised
// vector therefore scales every score by its length - producing scores that
// look reasonable, are ordered correctly, and sit below whatever threshold the
// user chose. Asserted so the requirement is not quietly dropped.
func TestZeroShotHeadRequiresNormalisedInput(t *testing.T) {
	head, err := NewZeroShotHead([]string{"x"}, 2, []float32{1, 0}, 1)
	if err != nil {
		t.Fatal(err)
	}

	scores := make([]float32, 1)
	if err := head.Score([]float32{0.5, 0}, scores); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(scores[0]-0.5)) > 1e-6 {
		t.Errorf("a half-length vector scored %v; the head does not normalise its input", scores[0])
	}

	normalised := []float32{0.5, 0}
	NormalizeInPlace(normalised, 2)
	if err := head.Score(normalised, scores); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(scores[0]-1)) > 1e-6 {
		t.Errorf("after normalising, score = %v, want 1", scores[0])
	}
}

func TestTrainedHeadUsesSigmoid(t *testing.T) {
	// One label, dimension 2, weights [1, 0] and bias 0.
	head, err := NewHead([]string{"x"}, 2, []float32{1, 0, 0})
	if err != nil {
		t.Fatal(err)
	}

	scores := make([]float32, 1)
	if err := head.Score([]float32{0, 0}, scores); err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(scores[0]-0.5)) > 1e-6 {
		t.Errorf("a zero logit gave %v, want 0.5", scores[0])
	}

	if err := head.Score([]float32{100, 0}, scores); err != nil {
		t.Fatal(err)
	}
	if scores[0] < 0.999 {
		t.Errorf("a large positive logit gave %v, want nearly 1", scores[0])
	}
}

// A large negative logit overflows exp() to +Inf and yields NaN rather than the
// zero it should. That NaN then spreads through every comparison downstream.
func TestSigmoidDoesNotOverflow(t *testing.T) {
	head, err := NewHead([]string{"x"}, 1, []float32{1, 0})
	if err != nil {
		t.Fatal(err)
	}

	scores := make([]float32, 1)
	for _, input := range []float32{-1e30, 1e30, -1000, 1000} {
		if err := head.Score([]float32{input}, scores); err != nil {
			t.Fatal(err)
		}
		v := float64(scores[0])
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("input %v produced %v", input, scores[0])
		}
		if v < 0 || v > 1 {
			t.Errorf("input %v produced %v, outside [0,1]", input, scores[0])
		}
	}
}

func TestApplyThresholdsAndSorts(t *testing.T) {
	labels := []string{"low", "high", "mid"}
	// Each label points along a different axis, with different magnitudes so
	// the ordering is unambiguous.
	head, err := NewHead(labels, 3, []float32{
		1, 0, 0, -2, // low: sigmoid(x-2)
		0, 1, 0, 2, // high: sigmoid(y+2)
		0, 0, 1, 0, // mid: sigmoid(z)
	})
	if err != nil {
		t.Fatal(err)
	}

	hits, _, err := head.Apply([]float32{1, 1, 1}, 0.5, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(hits) != 2 {
		t.Fatalf("hits = %+v, want the two above threshold", hits)
	}
	if hits[0].Label != "high" {
		t.Errorf("hits are not sorted by confidence: %+v", hits)
	}
	for _, hit := range hits {
		if hit.Label == "low" {
			t.Error("a label below the threshold was returned")
		}
	}
}

func TestHeadRejectsAMalformedWeightBlock(t *testing.T) {
	if _, err := NewHead([]string{"a", "b"}, 4, make([]float32, 9)); err == nil {
		t.Error("a weight block of the wrong size was accepted")
	}
	if _, err := NewHead(nil, 4, nil); err == nil {
		t.Error("a head with no labels was accepted")
	}
	if _, err := NewZeroShotHead([]string{"a"}, 4, make([]float32, 3), 1); err == nil {
		t.Error("label embeddings of the wrong size were accepted")
	}
}

func TestNormalizeInPlace(t *testing.T) {
	vectors := []float32{3, 4, 0, 0, 0, 0, 1, 0, 0}
	NormalizeInPlace(vectors, 3)

	// 3,4,0 has length 5.
	if math.Abs(float64(vectors[0]-0.6)) > 1e-6 || math.Abs(float64(vectors[1]-0.8)) > 1e-6 {
		t.Errorf("first vector = %v, want 0.6,0.8,0", vectors[:3])
	}
	// A zero vector cannot be normalised; leaving it alone beats NaNs that
	// spread through every later comparison.
	for _, v := range vectors[3:6] {
		if math.IsNaN(float64(v)) {
			t.Fatal("a zero vector produced NaN")
		}
	}
	if vectors[6] != 1 {
		t.Errorf("an already-unit vector changed: %v", vectors[6:])
	}
}

func TestCosineSimilarity(t *testing.T) {
	if got := CosineSimilarity([]float32{1, 0}, []float32{1, 0}); math.Abs(float64(got-1)) > 1e-6 {
		t.Errorf("identical vectors scored %v, want 1", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{0, 1}); math.Abs(float64(got)) > 1e-6 {
		t.Errorf("orthogonal vectors scored %v, want 0", got)
	}
	if got := CosineSimilarity([]float32{1, 0}, []float32{-1, 0}); math.Abs(float64(got+1)) > 1e-6 {
		t.Errorf("opposed vectors scored %v, want -1", got)
	}
	// Mismatched lengths are a programming error; returning zero is safer than
	// reading past the end of a slice.
	if got := CosineSimilarity([]float32{1, 0}, []float32{1}); got != 0 {
		t.Errorf("mismatched lengths scored %v, want 0", got)
	}
}
