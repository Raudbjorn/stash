package metadata

import (
	"fmt"
	"math"
	"strings"

	"github.com/stashapp/stash/pkg/scene/metadata/embedding"
)

// plausibilitySigmoidScale controls how sharply EmbeddingNamePlausibilityScorer
// maps the plausible-vs-implausible similarity gap into [0, 1]. Chosen from
// hands-on measurement against the exemplar sets below: real name pairs
// separate from marketing-copy bigrams by a similarity gap of roughly
// 0.1-0.3, and a scale of 6 maps that range to scores comfortably on either
// side of the analyzer's default 0.3 candidate-plausibility gate
// (internal/manager/task_analyze_scene_metadata.go's minCandidatePlausibility).
const plausibilitySigmoidScale = 6.0

// plausibleNameExemplars are two-word strings that are unambiguously real
// personal names, spanning a few different naming conventions. Averaged
// into a single reference embedding once at scorer construction.
var plausibleNameExemplars = []string{
	"Jane Doe", "John Smith", "Emily Clark", "Michael Brown", "Sarah Johnson",
	"David Lee", "Maria Garcia", "James Wilson", "Anna Kim", "Robert Davis",
	"Jessica Martinez", "Daniel Nguyen", "Laura Rodriguez", "Chris Evans",
	"Olivia Taylor", "Kevin Patel", "Rachel Adams", "Brian Thompson",
	"Natalie White", "Mark Anderson",
}

// implausiblePhraseExemplars are two-word strings typical of marketing copy
// that survives the title-case heuristic in ExtractNameCandidates but is
// not a personal name.
var implausiblePhraseExemplars = []string{
	"Exclusive Behind", "Amazing Scene", "First Time", "Behind Scenes",
	"Ultimate Collection", "Perfect Night", "Extreme Compilation",
	"Full Video", "Best Moments", "Live Show", "New Release",
	"Studio Update", "Latest Upload", "Members Only", "Bonus Footage",
}

// EmbeddingNamePlausibilityScorer scores candidates by comparing their
// sentence embedding against precomputed "plausible name" and "implausible
// phrase" reference centroids. A genuine drop-in for
// HeuristicNamePlausibilityScorer behind the NamePlausibilityScorer
// interface - same signature, same [0, 1] soft-filter semantics.
type EmbeddingNamePlausibilityScorer struct {
	model               *embedding.Model
	plausibleCentroid   []float32
	implausibleCentroid []float32
}

// NewEmbeddingNamePlausibilityScorer precomputes the reference centroids
// once against the loaded model. Returns an error if any exemplar fails to
// embed - callers should fall back to HeuristicNamePlausibilityScorer in
// that case rather than run with a partially-built scorer.
func NewEmbeddingNamePlausibilityScorer(model *embedding.Model) (*EmbeddingNamePlausibilityScorer, error) {
	plausibleCentroid, err := centroidEmbedding(model, plausibleNameExemplars)
	if err != nil {
		return nil, fmt.Errorf("embedding plausible-name exemplars: %w", err)
	}

	implausibleCentroid, err := centroidEmbedding(model, implausiblePhraseExemplars)
	if err != nil {
		return nil, fmt.Errorf("embedding implausible-phrase exemplars: %w", err)
	}

	return &EmbeddingNamePlausibilityScorer{
		model:               model,
		plausibleCentroid:   plausibleCentroid,
		implausibleCentroid: implausibleCentroid,
	}, nil
}

func (s *EmbeddingNamePlausibilityScorer) Score(candidate string) float64 {
	if len(strings.Fields(candidate)) != 2 {
		// candidates from ExtractNameCandidates are always 2 words; anything
		// else is out of scope for this scorer (matches
		// HeuristicNamePlausibilityScorer's contract).
		return 0
	}

	emb, err := s.model.Embed(candidate)
	if err != nil {
		// Conservative: treat an inference failure as implausible rather
		// than propagating an error through the NamePlausibilityScorer
		// interface, which has no error return.
		return 0
	}

	simPlausible := cosineSimilarity(emb, s.plausibleCentroid)
	simImplausible := cosineSimilarity(emb, s.implausibleCentroid)
	diff := simPlausible - simImplausible

	return 1 / (1 + math.Exp(-plausibilitySigmoidScale*diff))
}

func centroidEmbedding(model *embedding.Model, examples []string) ([]float32, error) {
	var sum []float32

	for _, example := range examples {
		emb, err := model.Embed(example)
		if err != nil {
			return nil, fmt.Errorf("embedding %q: %w", example, err)
		}
		if sum == nil {
			sum = make([]float32, len(emb))
		}
		for i, v := range emb {
			sum[i] += v
		}
	}

	for i := range sum {
		sum[i] /= float32(len(examples))
	}

	return sum, nil
}

func cosineSimilarity(a, b []float32) float64 {
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}
