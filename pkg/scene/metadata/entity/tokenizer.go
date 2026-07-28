package entity

import (
	"fmt"
	"path/filepath"
	"unicode"
	"unicode/utf8"

	hftokenizer "github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

const (
	maxWords = 384
	maxWidth = 12
	entToken = "<<ENT>>"
	sepToken = "<<SEP>>"
)

var Labels = []string{
	"adult performer name",
	"production studio",
	"production date",
	"movie title",
	"video encoding group",
}

type word struct {
	Text      string
	RuneStart int
	RuneEnd   int
	ByteStart int
	ByteEnd   int
}

type modelInputs struct {
	Words         []word
	InputIDs      []int64
	AttentionMask []int64
	WordsMask     []int64
	TextLengths   []int64
	SpanIdx       []int64
	SpanMask      []bool
}

type glinerTokenizer struct {
	tokenizer *hftokenizer.Tokenizer
}

func loadTokenizer(bundlePath string) (*glinerTokenizer, error) {
	tokenizer, err := pretrained.FromFile(filepath.Join(bundlePath, "tokenizer.json"))
	if err != nil {
		return nil, fmt.Errorf("load tokenizer.json: %w", err)
	}
	return &glinerTokenizer{tokenizer: tokenizer}, nil
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r)
}

// splitWords matches GLiNER's pinned Python whitespace splitter:
// `\w+(?:[-_]\w+)*|\S`, with Unicode-aware \w and half-open offsets.
func splitWords(text string) []word {
	runes := []rune(text)
	byteOffsets := make([]int, len(runes)+1)
	byteIndex := 0
	for index, r := range runes {
		byteOffsets[index] = byteIndex
		byteIndex += utf8.RuneLen(r)
	}
	byteOffsets[len(runes)] = len(text)

	words := make([]word, 0, len(runes)/2)
	for index := 0; index < len(runes); {
		if unicode.IsSpace(runes[index]) {
			index++
			continue
		}
		start := index
		if isWordRune(runes[index]) {
			index++
			for index < len(runes) {
				if isWordRune(runes[index]) {
					index++
					continue
				}
				if runes[index] == '-' && index+1 < len(runes) && isWordRune(runes[index+1]) {
					index += 2
					for index < len(runes) && isWordRune(runes[index]) {
						index++
					}
					continue
				}
				break
			}
		} else {
			index++
		}
		words = append(words, word{
			Text: string(runes[start:index]), RuneStart: start, RuneEnd: index,
			ByteStart: byteOffsets[start], ByteEnd: byteOffsets[index],
		})
	}
	return words
}

func (t *glinerTokenizer) build(text string, labels []string) (modelInputs, error) {
	words := splitWords(text)
	if len(words) > maxWords {
		words = words[:maxWords]
	}
	promptLength := len(labels)*2 + 1
	pieces := make([]string, 0, promptLength+len(words))
	for _, label := range labels {
		pieces = append(pieces, entToken, label)
	}
	pieces = append(pieces, sepToken)
	for _, word := range words {
		pieces = append(pieces, word.Text)
	}

	encoding, err := t.tokenizer.Encode(hftokenizer.NewSingleEncodeInput(hftokenizer.NewInputSequence(pieces)), true)
	if err != nil {
		return modelInputs{}, fmt.Errorf("tokenize GLiNER input: %w", err)
	}
	input := modelInputs{
		Words: words, InputIDs: make([]int64, len(encoding.Ids)),
		AttentionMask: make([]int64, len(encoding.Ids)), WordsMask: make([]int64, len(encoding.Ids)),
		TextLengths: []int64{int64(len(words))},
		SpanIdx:     make([]int64, 0, len(words)*maxWidth*2),
		SpanMask:    make([]bool, 0, len(words)*maxWidth),
	}
	for index, id := range encoding.Ids {
		input.InputIDs[index] = int64(id)
		input.AttentionMask[index] = 1
	}

	// sugarme's word IDs diverge from Hugging Face for special tokens,
	// punctuation, and the final pretokenized word. Re-tokenize each bounded
	// pretokenized piece and align its exact IDs against the combined sequence.
	// This is the compatibility gate: any unsupported tokenizer behavior fails
	// closed instead of producing approximate masks.
	cursor := 1 // the post-processor prepends [CLS]
	for pieceIndex, piece := range pieces {
		pieceEncoding, pieceErr := t.tokenizer.Encode(
			hftokenizer.NewSingleEncodeInput(hftokenizer.NewInputSequence([]string{piece})), false,
		)
		if pieceErr != nil {
			return modelInputs{}, fmt.Errorf("tokenize GLiNER word %d: %w", pieceIndex, pieceErr)
		}
		if cursor+len(pieceEncoding.Ids) > len(input.InputIDs)-1 {
			return modelInputs{}, fmt.Errorf("tokenizer word alignment exceeds combined input at word %d", pieceIndex)
		}
		for offset, id := range pieceEncoding.Ids {
			if input.InputIDs[cursor+offset] != int64(id) {
				return modelInputs{}, fmt.Errorf("tokenizer word alignment mismatch at word %d token %d", pieceIndex, offset)
			}
		}
		if pieceIndex >= promptLength && len(pieceEncoding.Ids) != 0 {
			input.WordsMask[cursor] = int64(pieceIndex - promptLength + 1)
		}
		cursor += len(pieceEncoding.Ids)
	}
	if cursor != len(input.InputIDs)-1 {
		return modelInputs{}, fmt.Errorf("tokenizer word alignment consumed %d of %d tokens", cursor, len(input.InputIDs)-1)
	}
	for start := range words {
		for width := range maxWidth {
			end := start + width
			input.SpanIdx = append(input.SpanIdx, int64(start), int64(end))
			input.SpanMask = append(input.SpanMask, end < len(words))
		}
	}
	return input, nil
}
