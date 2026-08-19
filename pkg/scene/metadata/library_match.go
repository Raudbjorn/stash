package metadata

import (
	"sort"
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

type phrase struct {
	Norm  string
	Words []string
	ID    int
}

// ExactIndex owns the normalized library names and aliases for repeated scans.
type ExactIndex struct {
	ByKey   map[string]map[int]struct{}
	Phrases []phrase
}

// BuildExactIndex normalizes library records once. Phrases are longest-first,
// then alphabetical, so overlapping shorter names cannot displace a longer
// exact match.
func BuildExactIndex(records []NamedAliases) *ExactIndex {
	index := &ExactIndex{ByKey: make(map[string]map[int]struct{})}
	add := func(value string, id int) {
		key := NormalizeKey(value)
		if key == "" {
			return
		}
		ids := index.ByKey[key]
		if ids == nil {
			ids = make(map[int]struct{})
			index.ByKey[key] = ids
		}
		ids[id] = struct{}{}
	}
	for _, record := range records {
		add(record.Name, record.ID)
		for _, alias := range record.Aliases {
			add(alias, record.ID)
		}
	}

	index.Phrases = make([]phrase, 0, len(index.ByKey))
	for norm, ids := range index.ByKey {
		item := phrase{Norm: norm, Words: strings.Fields(norm)}
		if len(ids) == 1 {
			for id := range ids {
				item.ID = id
			}
		}
		index.Phrases = append(index.Phrases, item)
	}
	sort.Slice(index.Phrases, func(i, j int) bool {
		left, right := index.Phrases[i], index.Phrases[j]
		if len(left.Words) != len(right.Words) {
			return len(left.Words) > len(right.Words)
		}
		return left.Norm < right.Norm
	})
	return index
}

// Scan performs normalized whole-token matching and emits only names or aliases
// owned by one distinct library record. Ambiguous aliases never link.
func (i *ExactIndex) Scan(source Source, label string) []Span {
	if i == nil {
		return nil
	}
	tokens := lexicalTokens(source.RawText)
	if len(tokens) == 0 {
		return nil
	}

	var ret []Span
	occupied := make([]bool, len(tokens))
	for _, item := range i.Phrases {
		ids := i.ByKey[item.Norm]
		if len(ids) != 1 || len(item.Words) == 0 || len(item.Words) > len(tokens) {
			continue
		}
		for start := 0; start+len(item.Words) <= len(tokens); start++ {
			matches := true
			for offset, word := range item.Words {
				if occupied[start+offset] || tokens[start+offset].key != word {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			end := start + len(item.Words) - 1
			for tokenIndex := start; tokenIndex <= end; tokenIndex++ {
				occupied[tokenIndex] = true
			}
			entityID := item.ID
			ret = append(ret, Span{
				Source: source, ByteStart: tokens[start].byteStart, ByteEnd: tokens[end].byteEnd,
				RuneStart: tokens[start].runeStart, RuneEnd: tokens[end].runeEnd,
				Text: source.RawText[tokens[start].byteStart:tokens[end].byteEnd], NormalizedKey: item.Norm,
				Label: label, Score: 1, Kind: EvidenceExactLibraryMatch, EntityID: &entityID,
			})
		}
	}
	sort.SliceStable(ret, func(left, right int) bool {
		if ret[left].ByteStart != ret[right].ByteStart {
			return ret[left].ByteStart < ret[right].ByteStart
		}
		return ret[left].ByteEnd > ret[right].ByteEnd
	})
	return ret
}

// FindExactNamedSpans preserves the one-shot API for existing callers.
func FindExactNamedSpans(source Source, records []NamedAliases, label string) []Span {
	return BuildExactIndex(records).Scan(source, label)
}
