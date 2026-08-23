package metadata

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// wordSplitRE splits text on anything that isn't a letter, keeping
// contiguous runs of letters (in any script) as tokens.
var wordSplitRE = regexp.MustCompile(`[^\p{L}]+`)

// nonNameTokens are common non-name tokens that should never be treated as
// part of a candidate performer name, even when title-cased. Adult-content
// titles routinely sentence-case a marketing phrase or descriptor alongside
// the actual performer name, so this includes both common English words and
// recurring descriptor vocabulary. Extraction and plausibility scoring share
// this set so lexical boundaries and low-score guards cannot disagree.
var nonNameTokens = map[string]struct{}{
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
	"where": {}, "which": {}, "while": {}, "after": {}, "before": {},
	"about": {}, "against": {}, "between": {}, "into": {}, "through": {},
	"during": {}, "without": {}, "under": {}, "over": {},
	"eyes": {}, "friends": {}, "secret": {},

	// recurring adult-content descriptor/genre vocabulary that is not a name
	"skinny": {}, "busty": {}, "chubby": {}, "curvy": {}, "tiny": {}, "thicc": {},
	"petite": {}, "blonde": {}, "brunette": {}, "redhead": {}, "hot": {},
	"sexy": {}, "cute": {}, "wild": {}, "naughty": {}, "innocent": {},
	"young": {}, "old": {}, "mature": {}, "big": {}, "small": {}, "huge": {},
	"tight": {}, "wet": {}, "hard": {}, "rough": {}, "gentle": {}, "sweet": {},
	"bad": {}, "good": {}, "real": {}, "first": {}, "second": {}, "third": {},
	"last": {}, "next": {}, "step": {}, "mom": {}, "dad": {}, "sister": {},
	"stepmom": {}, "stepsister": {}, "stepdad": {}, "teacher": {}, "boss": {},
	"neighbor": {}, "teen": {}, "milf": {}, "goth": {}, "punk": {}, "nerdy": {},
	"girls": {}, "boys": {}, "guys": {},
	"anal": {}, "xxx": {}, "amateur": {}, "public": {}, "pov": {}, "vr": {},
	"handjob": {}, "blowjob": {}, "squirt": {}, "creampie": {}, "pussy": {},
	"dick": {}, "sex": {}, "fuck": {}, "fucking": {}, "oral": {}, "lesbian": {},
	"gay": {}, "bisexual": {}, "threesome": {}, "orgy": {}, "gangbang": {},
	"massage": {}, "casting": {}, "interview": {}, "compilation": {},
	"trailer": {}, "clip": {},

	// marketing superlatives and temporal title fragments
	"amazing": {}, "beautiful": {}, "gorgeous": {}, "incredible": {},
	"exclusive": {}, "extreme": {}, "ultimate": {}, "perfect": {},
	"morning": {}, "night": {}, "day": {}, "week": {}, "year": {},
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

func isNonNameToken(w string) bool {
	_, ok := nonNameTokens[strings.ToLower(w)]
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
		if isNameLikeWord(w) && !isNonNameToken(w) {
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

var parentheticalNameRE = regexp.MustCompile(`^(.+?)\s*\(([^)]*)\)\s*$`)

// ImplausiblePerformerName reports names that must not be exact-matched,
// verified, or created. GLiNER spans bypass ExtractNameCandidates, so this
// gate is applied at library load, candidate collection, and resolution.
func ImplausiblePerformerName(name string) bool {
	return implausiblePerformerName(name, true)
}

func implausiblePerformerName(name string, unwrap bool) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return true
	}
	if unwrap {
		if match := parentheticalNameRE.FindStringSubmatch(name); match != nil {
			if implausiblePerformerName(match[1], false) || implausiblePerformerName(name, false) {
				return true
			}
		}
	}
	if utf8.RuneCountInString(name) == 1 {
		return true
	}
	key := NormalizeKey(name)
	if key == "" || utf8.RuneCountInString(key) == 1 {
		return true
	}
	tokens := strings.Fields(key)
	if len(tokens) == 0 {
		return true
	}
	if len(tokens) == 1 && isNonNameToken(tokens[0]) {
		return true
	}
	allStop := true
	for _, token := range tokens {
		if !isNonNameToken(token) {
			allStop = false
			break
		}
	}
	if allStop {
		return true
	}
	if technicalTokenName(name) || technicalTokenName(key) {
		return true
	}
	if releaseNameRE.MatchString(name) && utf8.RuneCountInString(name) <= 6 && !isNameLikeWord(name) {
		return true
	}
	return false
}

func technicalTokenName(name string) bool {
	match := technicalRE.FindStringSubmatch(name)
	return match != nil && strings.EqualFold(strings.TrimSpace(match[1]), strings.TrimSpace(name))
}
