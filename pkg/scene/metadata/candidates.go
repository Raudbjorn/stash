package metadata

import (
	"regexp"
	"strings"
	"unicode"
)

// wordSplitRE splits text on anything that isn't a letter, keeping
// contiguous runs of letters (in any script) as tokens.
var wordSplitRE = regexp.MustCompile(`[^\p{L}]+`)

// stopWords are common non-name tokens that should never be treated as
// part of a candidate performer name, even when title-cased.
var stopWords = map[string]struct{}{
	"the": {}, "and": {}, "with": {}, "featuring": {}, "feat": {},
	"scene": {}, "part": {}, "vol": {}, "volume": {}, "full": {}, "video": {},
	"official": {}, "site": {}, "rip": {}, "web": {}, "hd": {}, "sd": {},
	"new": {}, "best": {}, "top": {}, "of": {}, "for": {}, "in": {}, "on": {},
	"her": {}, "his": {}, "she": {}, "him": {}, "com": {}, "net": {},
}

// isNameLikeWord returns true if w looks like a capitalized proper-noun
// word: starts with an uppercase letter, the rest are lowercase, length >= 2.
func isNameLikeWord(w string) bool {
	runes := []rune(w)
	if len(runes) < 2 {
		return false
	}
	if !unicode.IsUpper(runes[0]) {
		return false
	}
	for _, r := range runes[1:] {
		if !unicode.IsLower(r) {
			return false
		}
	}
	return true
}

func isStopWord(w string) bool {
	_, ok := stopWords[strings.ToLower(w)]
	return ok
}

// ExtractNameCandidates scans normalized text for title-cased words (e.g.
// "Jane Doe") and returns each adjacent pair as a candidate performer name.
// Pairs are taken from non-overlapping runs of consecutive name-like words
// (a run of 4 words yields 2 pairs, never a sliding window), so a run like
// "Studio Name Jane Doe" can't produce a spurious crossover pair like "Name
// Jane". exclude, if non-nil, is called with the lowercased candidate (e.g.
// to drop the studio's own name); if it returns true the candidate is
// dropped.
//
// This is a deliberately crude heuristic: it will both miss real names and
// occasionally propose a non-name pair (e.g. a studio name). That's fine -
// every candidate this produces still has to survive matching against
// known performers or scraper verification before anything is written, so
// precision here matters less than not missing real names.
func ExtractNameCandidates(text string, exclude func(candidate string) bool) []string {
	words := wordSplitRE.Split(text, -1)

	var ret []string
	seen := map[string]struct{}{}

	addPair := func(w1, w2 string) {
		candidate := w1 + " " + w2
		key := strings.ToLower(candidate)
		if _, ok := seen[key]; ok {
			return
		}
		if exclude != nil && exclude(key) {
			return
		}
		seen[key] = struct{}{}
		ret = append(ret, candidate)
	}

	runStart := -1
	flushRun := func(end int) {
		if runStart == -1 {
			return
		}
		for i := runStart; i+1 < end; i += 2 {
			addPair(words[i], words[i+1])
		}
		runStart = -1
	}

	for i, w := range words {
		if isNameLikeWord(w) && !isStopWord(w) {
			if runStart == -1 {
				runStart = i
			}
			continue
		}
		flushRun(i)
	}
	flushRun(len(words))

	return ret
}
