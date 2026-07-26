// Package embedding provides a local, offline sentence-embedding model
// (a quantized MiniLM export run through ONNX Runtime) used as a soft
// plausibility signal for candidate performer names extracted from scene
// text. See pkg/scene/metadata/embedding_scorer.go for the consumer.
package embedding

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"unicode"
)

const (
	tokenPAD = "[PAD]"
	tokenUNK = "[UNK]"
	tokenCLS = "[CLS]"
	tokenSEP = "[SEP]"

	// wordPieceMaxChars bounds a single basic-token's length for wordpiece
	// matching; BERT's reference implementation treats anything longer as
	// unknown rather than doing unbounded backtracking.
	wordPieceMaxChars = 100
)

// Tokenizer implements BERT-style WordPiece tokenization against a fixed
// vocabulary, matching the preprocessing the MiniLM ONNX export expects.
// Deliberately dependency-free: this is a well-defined, unambiguous
// algorithm over a fixed vocab file, so there's no need for a CGO/Rust
// tokenizer library on top of the CGO already required for inference.
type Tokenizer struct {
	vocab map[string]int64
	padID int64
	unkID int64
	clsID int64
	sepID int64
}

// NewTokenizer builds a Tokenizer from a vocab.txt reader: one token per
// line, id == line number (0-based), matching the standard BERT vocab
// format.
func NewTokenizer(r io.Reader) (*Tokenizer, error) {
	vocab := make(map[string]int64)
	scanner := bufio.NewScanner(r)
	var id int64
	for scanner.Scan() {
		token := scanner.Text()
		if token != "" {
			vocab[token] = id
		}
		id++
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading vocab: %w", err)
	}

	t := &Tokenizer{vocab: vocab}

	var ok bool
	if t.padID, ok = vocab[tokenPAD]; !ok {
		return nil, fmt.Errorf("vocab missing %s", tokenPAD)
	}
	if t.unkID, ok = vocab[tokenUNK]; !ok {
		return nil, fmt.Errorf("vocab missing %s", tokenUNK)
	}
	if t.clsID, ok = vocab[tokenCLS]; !ok {
		return nil, fmt.Errorf("vocab missing %s", tokenCLS)
	}
	if t.sepID, ok = vocab[tokenSEP]; !ok {
		return nil, fmt.Errorf("vocab missing %s", tokenSEP)
	}

	return t, nil
}

// Encode tokenizes text into fixed-length input_ids/attention_mask/
// token_type_ids arrays of length maxLen, following the standard single-
// sequence BERT input layout: [CLS] wordpieces... [SEP] [PAD]...
func (t *Tokenizer) Encode(text string, maxLen int) (inputIDs, attentionMask, tokenTypeIDs []int64) {
	pieces := t.wordpieceTokenize(basicTokenize(text))

	inputIDs = make([]int64, maxLen)
	attentionMask = make([]int64, maxLen)
	tokenTypeIDs = make([]int64, maxLen) // single-sequence input: all zeros

	inputIDs[0] = t.clsID
	attentionMask[0] = 1
	pos := 1

	maxPieces := maxLen - 2 // room for [CLS] and [SEP]
	if len(pieces) > maxPieces {
		pieces = pieces[:maxPieces]
	}

	for _, id := range pieces {
		inputIDs[pos] = id
		attentionMask[pos] = 1
		pos++
	}

	inputIDs[pos] = t.sepID
	attentionMask[pos] = 1

	// Remaining slots stay zero-valued, which is exactly [PAD]'s id (0) and
	// an unset attention mask - correct without further work.
	_ = t.padID

	return inputIDs, attentionMask, tokenTypeIDs
}

// basicTokenize lowercases and splits text into words and individual
// punctuation characters, mirroring BERT's BasicTokenizer for the ASCII
// text this analyzer deals with (scene titles/filenames).
func basicTokenize(text string) []string {
	var tokens []string
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}

	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.IsSpace(r):
			flush()
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			flush()
			tokens = append(tokens, string(r))
		default:
			current.WriteRune(r)
		}
	}
	flush()

	return tokens
}

// wordpieceTokenize splits each basic token into vocab subwords using
// greedy longest-match-first, falling back to [UNK] for the whole token if
// any piece can't be matched - the standard BERT WordPiece algorithm.
func (t *Tokenizer) wordpieceTokenize(basicTokens []string) []int64 {
	var ids []int64

	for _, token := range basicTokens {
		runes := []rune(token)
		if len(runes) > wordPieceMaxChars {
			ids = append(ids, t.unkID)
			continue
		}

		pieceIDs, ok := t.greedyMatch(runes)
		if !ok {
			ids = append(ids, t.unkID)
			continue
		}
		ids = append(ids, pieceIDs...)
	}

	return ids
}

func (t *Tokenizer) greedyMatch(runes []rune) ([]int64, bool) {
	var pieceIDs []int64
	start := 0

	for start < len(runes) {
		end := len(runes)
		var matchedID int64
		found := false

		for end > start {
			candidate := string(runes[start:end])
			if start > 0 {
				candidate = "##" + candidate
			}
			if id, ok := t.vocab[candidate]; ok {
				matchedID = id
				found = true
				break
			}
			end--
		}

		if !found {
			return nil, false
		}

		pieceIDs = append(pieceIDs, matchedID)
		start = end
	}

	return pieceIDs, true
}
