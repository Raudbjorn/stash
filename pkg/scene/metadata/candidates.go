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
// part of a candidate performer name, even when title-cased. Adult-content
// titles routinely sentence-case a marketing phrase or a genre/descriptor
// tag alongside the actual performer name (e.g. "You Make Skinny Kenzie
// Reeves Cum"), so this list covers both common English function words and
// the recurring descriptor/genre vocabulary seen in real titles - not just
// generic stopwords - since either can otherwise misalign the non-overlapping
// pairing in ExtractNameCandidates away from the real name.
var stopWords = map[string]struct{}{
	// generic
	"the": {}, "and": {}, "with": {}, "featuring": {}, "feat": {},
	"scene": {}, "part": {}, "vol": {}, "volume": {}, "full": {}, "video": {},
	"official": {}, "site": {}, "rip": {}, "web": {}, "hd": {}, "sd": {},
	"new": {}, "best": {}, "top": {}, "of": {}, "for": {}, "in": {}, "on": {},
	"her": {}, "his": {}, "she": {}, "him": {}, "com": {}, "net": {},

	// common function/filler words that frequently appear title-cased at the
	// start of a sentence-cased marketing phrase
	"you": {}, "your": {}, "yours": {}, "my": {}, "mine": {}, "our": {}, "ours": {},
	"their": {}, "theirs": {}, "its": {}, "who": {}, "whom": {}, "whose": {},
	"make": {}, "makes": {}, "making": {}, "made": {},
	"get": {}, "gets": {}, "getting": {}, "got": {},
	"want": {}, "wants": {}, "wanting": {},
	"love": {}, "loves": {}, "loving": {},
	"come": {}, "comes": {}, "coming": {},
	"leave": {}, "leaves": {}, "leaving": {},
	"work": {}, "works": {}, "working": {},
	"every": {}, "each": {}, "time": {}, "times": {},
	"when": {}, "why": {}, "how": {}, "what": {}, "that": {}, "this": {},
	"these": {}, "those": {}, "are": {}, "is": {}, "was": {}, "were": {},
	"will": {}, "would": {}, "can": {}, "could": {}, "dont": {}, "wont": {},
	"cant": {}, "just": {}, "so": {}, "too": {}, "very": {}, "not": {},
	"now": {}, "then": {}, "than": {}, "more": {}, "most": {}, "some": {},
	"any": {}, "all": {}, "again": {}, "still": {}, "back": {}, "only": {},
	"eyes": {}, "friends": {}, "secret": {},

	// recurring adult-content descriptor/genre vocabulary that is not a name
	"skinny": {}, "busty": {}, "chubby": {}, "curvy": {}, "tiny": {}, "thicc": {},
	"petite": {}, "blonde": {}, "brunette": {}, "redhead": {}, "hot": {},
	"sexy": {}, "cute": {}, "wild": {}, "naughty": {}, "innocent": {},
	"young": {}, "old": {}, "mature": {}, "big": {}, "small": {}, "huge": {},
	"tight": {}, "wet": {}, "hard": {}, "rough": {}, "gentle": {}, "sweet": {},
	"bad": {}, "good": {}, "real": {}, "first": {}, "step": {}, "mom": {},
	"dad": {}, "sister": {}, "stepmom": {}, "stepsister": {}, "stepdad": {},
	"teacher": {}, "boss": {}, "neighbor": {}, "teen": {}, "milf": {},
	"goth": {}, "punk": {}, "nerdy": {}, "girls": {}, "boys": {}, "guys": {},
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
