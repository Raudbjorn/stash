package metadata

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type textToken struct {
	key       string
	byteStart int
	byteEnd   int
	runeStart int
	runeEnd   int
}

func lexicalTokens(text string) []textToken {
	var tokens []textToken
	startByte, startRune := -1, -1
	runeIndex := 0
	flush := func(endByte, endRune int) {
		if startByte < 0 {
			return
		}
		value := text[startByte:endByte]
		if key := NormalizeKey(value); key != "" {
			tokens = append(tokens, textToken{key: key, byteStart: startByte, byteEnd: endByte, runeStart: startRune, runeEnd: endRune})
		}
		startByte, startRune = -1, -1
	}
	for byteIndex, r := range text {
		wordRune := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' || r == '’'
		if wordRune {
			if startByte < 0 {
				startByte, startRune = byteIndex, runeIndex
			}
		} else {
			flush(byteIndex, runeIndex)
		}
		runeIndex++
	}
	flush(len(text), runeIndex)
	return tokens
}

// FindExactNamedSpans performs normalized whole-token matching and emits only
// names/aliases that belong to one distinct library record. Substrings and
// ambiguous aliases never link.
func FindExactNamedSpans(source Source, records []NamedAliases, label string) []Span {
	tokens := lexicalTokens(source.RawText)
	if len(tokens) == 0 {
		return nil
	}
	type patternRecord struct {
		words []string
		ids   map[int]struct{}
	}
	patterns := make(map[string]*patternRecord)
	for _, record := range records {
		values := append([]string{record.Name}, record.Aliases...)
		for _, value := range values {
			key := NormalizeKey(value)
			if key == "" {
				continue
			}
			pattern := patterns[key]
			if pattern == nil {
				pattern = &patternRecord{words: strings.Fields(key), ids: make(map[int]struct{})}
				patterns[key] = pattern
			}
			pattern.ids[record.ID] = struct{}{}
		}
	}
	keys := make([]string, 0, len(patterns))
	for key := range patterns {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var ret []Span
	seen := make(map[string]struct{})
	for _, key := range keys {
		pattern := patterns[key]
		if len(pattern.ids) != 1 || len(pattern.words) == 0 || len(pattern.words) > len(tokens) {
			continue
		}
		id := 0
		for candidateID := range pattern.ids {
			id = candidateID
		}
		for start := 0; start+len(pattern.words) <= len(tokens); start++ {
			matches := true
			for offset, word := range pattern.words {
				if tokens[start+offset].key != word {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			end := start + len(pattern.words) - 1
			dedupeKey := key + "\x00" + strconv.Itoa(id) + "\x00" + strconv.Itoa(tokens[start].byteStart)
			if _, duplicate := seen[dedupeKey]; duplicate {
				continue
			}
			seen[dedupeKey] = struct{}{}
			entityID := id
			ret = append(ret, Span{
				Source: source, ByteStart: tokens[start].byteStart, ByteEnd: tokens[end].byteEnd,
				RuneStart: tokens[start].runeStart, RuneEnd: tokens[end].runeEnd,
				Text: source.RawText[tokens[start].byteStart:tokens[end].byteEnd], NormalizedKey: key,
				Label: label, Score: 1, Kind: EvidenceExactLibraryMatch, EntityID: &entityID,
			})
		}
	}
	sort.SliceStable(ret, func(i, j int) bool {
		if ret[i].ByteStart != ret[j].ByteStart {
			return ret[i].ByteStart < ret[j].ByteStart
		}
		return ret[i].ByteEnd > ret[j].ByteEnd
	})
	return ret
}
