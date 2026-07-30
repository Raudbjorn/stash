package metadata

import (
	"strings"
	"unicode"
)

// NamePlausibilityScorer scores how plausible it is that a candidate string
// is a real personal name, as a soft filter on top of the regex-based
// candidate extraction in ExtractNameCandidates. Returns a score in [0, 1].
//
// Two implementations exist behind this interface:
//   - HeuristicNamePlausibilityScorer: dependency-free stopword/structure
//     heuristics. Always available, used as a fallback.
//   - EmbeddingNamePlausibilityScorer (embedding_scorer.go): scores via a
//     local MiniLM sentence-embedding model run through ONNX Runtime.
//     Requires the onnxruntime shared library at runtime (not build time -
//     see pkg/scene/metadata/embedding and
//     internal/manager/scene_metadata_models.go for how the caller
//     selects between the two).
type NamePlausibilityScorer interface {
	Score(candidate string) float64
}

// commonEnglishWords are frequent non-name English words that survive the
// title-case heuristic in ExtractNameCandidates (e.g. at the start of a
// sentence, or in ALL-CAPS-then-titlecased marketing text) but are not
// personal names. This is intentionally short - it's a cheap sanity check,
// not a dictionary; the scraper-verification step downstream is the real
// precision gate for genuinely new names.
var commonEnglishWords = map[string]struct{}{
	"this": {}, "that": {}, "these": {}, "those": {}, "what": {}, "when": {},
	"where": {}, "which": {}, "while": {}, "after": {}, "before": {},
	"about": {}, "again": {}, "against": {}, "between": {}, "into": {},
	"through": {}, "during": {}, "without": {}, "under": {}, "over": {},
	"first": {}, "second": {}, "third": {}, "last": {}, "next": {},
	"amazing": {}, "beautiful": {}, "gorgeous": {}, "incredible": {},
	"exclusive": {}, "extreme": {}, "ultimate": {}, "perfect": {},
	"morning": {}, "night": {}, "day": {}, "week": {}, "year": {},
}

// HeuristicNamePlausibilityScorer scores candidates using cheap structural
// and lexical heuristics: word count, length, absence of digits, and a
// small stoplist of common non-name English words. It deliberately errs
// towards not rejecting plausible-looking candidates, since the downstream
// matching/scraper-verification step is the actual precision gate.
type HeuristicNamePlausibilityScorer struct{}

func (HeuristicNamePlausibilityScorer) Score(candidate string) float64 {
	words := strings.Fields(candidate)
	if len(words) != 2 {
		// candidates from ExtractNameCandidates are always 2 words; anything
		// else is out of scope for this scorer.
		return 0
	}

	score := 0.7

	for _, w := range words {
		lower := strings.ToLower(w)
		if _, common := commonEnglishWords[lower]; common {
			return 0.1
		}

		runeCount := len([]rune(w))
		if runeCount < 3 {
			score -= 0.15
		}
		if runeCount > 12 {
			score -= 0.1
		}

		for _, r := range w {
			if unicode.IsDigit(r) {
				return 0
			}
		}
	}

	// a short, alliterative-ish first/last name pair is a common (though
	// not universal) pattern in this domain; give it a small nudge.
	if strings.EqualFold(string([]rune(words[0])[0]), string([]rune(words[1])[0])) {
		score += 0.05
	}

	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}

	return score
}
