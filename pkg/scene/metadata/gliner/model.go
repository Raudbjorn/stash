package gliner

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
	ort "github.com/yalue/onnxruntime_go"
)

const (
	modelRelativePath     = "onnx/model_int8.onnx"
	tokenizerRelativePath = "tokenizer.json"
	modelSHA256           = "995058c82c5f570601dd8a0ba74ee60a392f764268bc5f628455e44dd3b476ec"
	tokenizerSHA256       = "914bd3c8fb7b525af9e23b60d0ec7b1248ddb2b99014efd9c02ebeb022f8cab7"
	maxWords              = 384
	maxSpanWidth          = 12
)

var (
	ErrClosed = errors.New("GLiNER model is closed")

	inputNames  = []string{"input_ids", "attention_mask", "words_mask", "text_lengths", "span_idx", "span_mask"}
	outputNames = []string{"logits"}
)

// ProductionLabels is the fixed class order used by the scene metadata model.
var ProductionLabels = []string{
	"person",
	"production studio",
	"production date",
	"scene title",
	"movie title",
	"scene number",
	"release group",
}

// Span is an extracted entity with half-open UTF-8 byte offsets into the
// original input text.
type Span struct {
	Text  string
	Label string
	Start int
	End   int
	Score float32
}

type sourceWord struct {
	text       string
	start, end int
}

// Model wraps the external GLiNER graph. The process-wide ONNX Runtime
// environment must already be initialized by the caller.
type Model struct {
	mu         sync.Mutex
	closed     bool
	tokenizer  *tokenizer.Tokenizer
	session    *ort.DynamicAdvancedSession
	outputRank int
}

// Load verifies the pinned model assets and opens the external ONNX graph.
func Load(modelPath string) (*Model, error) {
	graphPath := filepath.Join(modelPath, filepath.FromSlash(modelRelativePath))
	tokenizerPath := filepath.Join(modelPath, tokenizerRelativePath)
	if err := verifySHA256(graphPath, modelSHA256); err != nil {
		return nil, fmt.Errorf("verifying GLiNER graph: %w", err)
	}
	if err := verifySHA256(tokenizerPath, tokenizerSHA256); err != nil {
		return nil, fmt.Errorf("verifying GLiNER tokenizer: %w", err)
	}

	tok, err := pretrained.FromFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("loading GLiNER tokenizer: %w", err)
	}

	inputs, outputs, err := ort.GetInputOutputInfo(graphPath)
	if err != nil {
		return nil, fmt.Errorf("reading GLiNER graph contract: %w", err)
	}
	outputRank, err := validateGraphContract(inputs, outputs)
	if err != nil {
		return nil, err
	}

	session, err := ort.NewDynamicAdvancedSession(graphPath, inputNames, outputNames, nil)
	if err != nil {
		return nil, fmt.Errorf("creating GLiNER session: %w", err)
	}

	return &Model{tokenizer: tok, session: session, outputRank: outputRank}, nil
}

func verifySHA256(path, expected string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := fmt.Sprintf("%x", h.Sum(nil))
	if actual != expected {
		return fmt.Errorf("%s has SHA-256 %s, expected %s", path, actual, expected)
	}
	return nil
}

func validateGraphContract(inputs, outputs []ort.InputOutputInfo) (int, error) {
	expectedInputs := []struct {
		name  string
		type_ ort.TensorElementDataType
		rank  int
	}{
		{"input_ids", ort.TensorElementDataTypeInt64, 2},
		{"attention_mask", ort.TensorElementDataTypeInt64, 2},
		{"words_mask", ort.TensorElementDataTypeInt64, 2},
		{"text_lengths", ort.TensorElementDataTypeInt64, 2},
		{"span_idx", ort.TensorElementDataTypeInt64, 3},
		{"span_mask", ort.TensorElementDataTypeBool, 2},
	}
	if len(inputs) != len(expectedInputs) {
		return 0, fmt.Errorf("GLiNER graph has %d inputs, expected %d", len(inputs), len(expectedInputs))
	}
	for i, expected := range expectedInputs {
		actual := inputs[i]
		if actual.Name != expected.name || actual.OrtValueType != ort.ONNXTypeTensor || actual.DataType != expected.type_ || len(actual.Dimensions) != expected.rank {
			return 0, fmt.Errorf("GLiNER input %d is %s, expected %s tensor rank %d", i, actual.String(), expected.name, expected.rank)
		}
		for _, dimension := range actual.Dimensions {
			if dimension > 0 {
				return 0, fmt.Errorf("GLiNER input %s has non-dynamic dimensions %v", actual.Name, actual.Dimensions)
			}
		}
	}
	var logits *ort.InputOutputInfo
	for i := range outputs {
		if outputs[i].Name == "logits" {
			logits = &outputs[i]
			break
		}
	}
	if logits == nil || logits.OrtValueType != ort.ONNXTypeTensor || logits.DataType != ort.TensorElementDataTypeFloat {
		return 0, fmt.Errorf("GLiNER logits output contract mismatch: %v", outputs)
	}
	rank := len(logits.Dimensions)
	if rank != 3 && rank != 4 {
		return 0, fmt.Errorf("GLiNER logits rank is %d, expected 3 or 4", rank)
	}
	for _, dimension := range logits.Dimensions {
		if dimension > 0 {
			return 0, fmt.Errorf("GLiNER logits have non-dynamic dimensions %v", logits.Dimensions)
		}
	}
	return rank, nil
}

// Close releases the GLiNER session. It is safe to call more than once.
func (m *Model) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil
	}
	m.closed = true
	return m.session.Destroy()
}

// Extract returns entity spans in source order. Calls are serialized because
// ONNX Runtime sessions are not assumed safe for concurrent Run calls.
func (m *Model) Extract(ctx context.Context, text string, labels []string, threshold float32) ([]Span, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(labels) == 0 || len(labels) != len(ProductionLabels) {
		return nil, fmt.Errorf("GLiNER requires the %d ordered production labels", len(ProductionLabels))
	}
	for i := range labels {
		if labels[i] != ProductionLabels[i] {
			return nil, fmt.Errorf("GLiNER label %d is %q, expected %q", i, labels[i], ProductionLabels[i])
		}
	}

	words := splitSourceWords(text, maxWords)
	if len(words) == 0 {
		return nil, nil
	}
	encoded, err := encodeInput(m.tokenizer, words, labels)
	if err != nil {
		return nil, err
	}
	spanIdx, spanMask := prepareSpanIndices(len(words), maxSpanWidth)

	inputIDs, err := ort.NewTensor(ort.NewShape(1, int64(len(encoded.inputIDs))), encoded.inputIDs)
	if err != nil {
		return nil, fmt.Errorf("creating input_ids tensor: %w", err)
	}
	defer inputIDs.Destroy()
	attention, err := ort.NewTensor(ort.NewShape(1, int64(len(encoded.attentionMask))), encoded.attentionMask)
	if err != nil {
		return nil, fmt.Errorf("creating attention_mask tensor: %w", err)
	}
	defer attention.Destroy()
	wordsMask, err := ort.NewTensor(ort.NewShape(1, int64(len(encoded.wordsMask))), encoded.wordsMask)
	if err != nil {
		return nil, fmt.Errorf("creating words_mask tensor: %w", err)
	}
	defer wordsMask.Destroy()
	textLengths, err := ort.NewTensor(ort.NewShape(1, 1), []int64{int64(len(words))})
	if err != nil {
		return nil, fmt.Errorf("creating text_lengths tensor: %w", err)
	}
	defer textLengths.Destroy()
	spanIndices, err := ort.NewTensor(ort.NewShape(1, int64(len(spanMask)), 2), spanIdx)
	if err != nil {
		return nil, fmt.Errorf("creating span_idx tensor: %w", err)
	}
	defer spanIndices.Destroy()
	spanMasks, err := ort.NewTensor(ort.NewShape(1, int64(len(spanMask))), spanMask)
	if err != nil {
		return nil, fmt.Errorf("creating span_mask tensor: %w", err)
	}
	defer spanMasks.Destroy()

	outputs := []ort.Value{nil}
	if err := m.session.Run([]ort.Value{inputIDs, attention, wordsMask, textLengths, spanIndices, spanMasks}, outputs); err != nil {
		return nil, fmt.Errorf("running GLiNER inference: %w", err)
	}
	if outputs[0] == nil {
		return nil, errors.New("GLiNER inference returned no logits")
	}
	defer outputs[0].Destroy()
	logits, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, fmt.Errorf("GLiNER logits have unexpected Go type %T", outputs[0])
	}
	return decodeSpans(text, words, labels, threshold, spanMask, logits.GetShape(), logits.GetData(), m.outputRank)
}

type encodedInput struct {
	inputIDs, attentionMask, wordsMask []int64
}

func encodeInput(tok *tokenizer.Tokenizer, words []sourceWord, labels []string) (encodedInput, error) {
	promptWordCount := len(labels)*2 + 1
	sequence := make([]string, 0, promptWordCount+len(words))
	for _, label := range labels {
		sequence = append(sequence, "<<ENT>>", label)
	}
	sequence = append(sequence, "<<SEP>>")
	for _, word := range words {
		sequence = append(sequence, word.text)
	}

	input := tokenizer.NewSingleEncodeInput(tokenizer.NewInputSequence(sequence))
	encoding, err := tok.Encode(input, true)
	if err != nil {
		return encodedInput{}, fmt.Errorf("tokenizing GLiNER input: %w", err)
	}
	if len(encoding.Ids) != len(encoding.AttentionMask) || len(encoding.Ids) != len(encoding.SpecialTokenMask) {
		return encodedInput{}, fmt.Errorf("GLiNER tokenizer returned inconsistent encoding lengths: ids=%d attention=%d special=%d", len(encoding.Ids), len(encoding.AttentionMask), len(encoding.SpecialTokenMask))
	}
	alignedWords := make([]int, len(encoding.Ids))
	wordIndex := 0
	for i, special := range encoding.SpecialTokenMask {
		alignedWords[i] = -1
		if special != 0 {
			continue
		}
		if wordIndex >= len(encoding.Words) {
			return encodedInput{}, errors.New("GLiNER tokenizer omitted word identifiers")
		}
		alignedWords[i] = encoding.Words[wordIndex]
		wordIndex++
	}
	if wordIndex != len(encoding.Words) {
		return encodedInput{}, fmt.Errorf("GLiNER tokenizer returned %d unused word identifiers", len(encoding.Words)-wordIndex)
	}

	ret := encodedInput{
		inputIDs:      make([]int64, len(encoding.Ids)),
		attentionMask: make([]int64, len(encoding.Ids)),
		wordsMask:     make([]int64, len(encoding.Ids)),
	}
	seenWords := make(map[int]struct{}, len(words))
	for i := range encoding.Ids {
		ret.inputIDs[i] = int64(encoding.Ids[i])
		ret.attentionMask[i] = int64(encoding.AttentionMask[i])
		wordID := alignedWords[i]
		textWord := wordID - promptWordCount
		if textWord < 0 || textWord >= len(words) {
			continue
		}
		if _, seen := seenWords[textWord]; seen {
			continue
		}
		seenWords[textWord] = struct{}{}
		ret.wordsMask[i] = int64(textWord + 1)
	}
	if len(seenWords) != len(words) {
		return encodedInput{}, fmt.Errorf("GLiNER tokenizer retained %d of %d source words", len(seenWords), len(words))
	}
	return ret, nil
}

func splitSourceWords(text string, limit int) []sourceWord {
	words := make([]sourceWord, 0)
	start := -1
	for offset := 0; offset < len(text); {
		r, size := utf8.DecodeRuneInString(text[offset:])
		if unicode.IsSpace(r) {
			if start >= 0 {
				words = append(words, sourceWord{text: text[start:offset], start: start, end: offset})
				if len(words) == limit {
					return words
				}
				start = -1
			}
		} else if start < 0 {
			start = offset
		}
		offset += size
	}
	if start >= 0 && len(words) < limit {
		words = append(words, sourceWord{text: text[start:], start: start, end: len(text)})
	}
	return words
}

func prepareSpanIndices(wordCount, width int) ([]int64, []bool) {
	indices := make([]int64, 0, wordCount*width*2)
	mask := make([]bool, 0, wordCount*width)
	for start := range wordCount {
		for spanWidth := range width {
			end := start + spanWidth
			indices = append(indices, int64(start), int64(end))
			mask = append(mask, end < wordCount)
		}
	}
	return indices, mask
}

func decodeSpans(text string, words []sourceWord, labels []string, threshold float32, spanMask []bool, shape ort.Shape, logits []float32, declaredRank int) ([]Span, error) {
	if len(shape) != declaredRank {
		return nil, fmt.Errorf("GLiNER logits rank %d differs from declared rank %d", len(shape), declaredRank)
	}
	spanCount := len(spanMask)
	labelCount := len(labels)
	if declaredRank == 4 {
		if len(shape) != 4 || shape[0] != 1 || shape[1] != int64(len(words)) || shape[2] != maxSpanWidth || shape[3] != int64(labelCount) {
			return nil, fmt.Errorf("unexpected GLiNER rank-4 logits shape %v", shape)
		}
	} else if len(shape) != 3 || shape[0] != 1 || shape[1] != int64(spanCount) || shape[2] != int64(labelCount) {
		return nil, fmt.Errorf("unexpected GLiNER rank-3 logits shape %v", shape)
	}
	if len(logits) != spanCount*labelCount {
		return nil, fmt.Errorf("GLiNER returned %d logits, expected %d", len(logits), spanCount*labelCount)
	}

	candidates := make([]Span, 0)
	for span := range spanCount {
		if !spanMask[span] {
			continue
		}
		startWord := span / maxSpanWidth
		endWord := startWord + span%maxSpanWidth
		for labelIndex, label := range labels {
			score := float32(1 / (1 + math.Exp(-float64(logits[span*labelCount+labelIndex]))))
			if score < threshold {
				continue
			}
			start, end := words[startWord].start, words[endWord].end
			candidates = append(candidates, Span{Text: text[start:end], Label: label, Start: start, End: end, Score: score})
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Score > candidates[j].Score })
	kept := make([]Span, 0, len(candidates))
	for _, candidate := range candidates {
		overlaps := false
		for _, existing := range kept {
			if candidate.Label == existing.Label && candidate.Start < existing.End && existing.Start < candidate.End {
				overlaps = true
				break
			}
		}
		if !overlaps {
			kept = append(kept, candidate)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Start != kept[j].Start {
			return kept[i].Start < kept[j].Start
		}
		if kept[i].End != kept[j].End {
			return kept[i].End < kept[j].End
		}
		return kept[i].Label < kept[j].Label
	})
	return kept, nil
}
