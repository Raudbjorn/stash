package metadata

import (
	"strings"
	"unicode"
)

// NamePlausibilityScorer is the dependency-free structural guard used only
// for heuristic two-word candidates from ExtractNameCandidates. Typed NFO
// actors and GLiNER spans use their own trust/confidence evidence instead.
// Scores are in [0, 1].
type NamePlausibilityScorer interface {
	Score(candidate string) float64
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
		if isNonNameToken(w) {
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
