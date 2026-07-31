package entity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type tokenizerFixture struct {
	Labels []string `json:"labels"`
	Cases  []struct {
		Name          string  `json:"name"`
		Text          *string `json:"text"`
		GeneratedText *struct {
			Prefix string `json:"prefix"`
			Count  int    `json:"count"`
		} `json:"generated_text"`
		Words         []string `json:"words"`
		WordOffsets   [][]int  `json:"word_offsets"`
		InputIDs      []int64  `json:"input_ids"`
		AttentionMask []int64  `json:"attention_mask"`
		WordsMask     []int64  `json:"words_mask"`
		TextLengths   []int64  `json:"text_lengths"`
		SpanIdxShape  []int    `json:"span_idx_shape"`
		SpanIdxHash   string   `json:"span_idx_sha256_le_i64"`
		SpanMaskShape []int    `json:"span_mask_shape"`
		SpanMaskHash  string   `json:"span_mask_sha256_bytes"`
	} `json:"cases"`
}

func referenceBundle(t testing.TB) string {
	t.Helper()
	candidates := []string{os.Getenv("GLINER_MODEL_DIR"), "/tmp/gliner-reference"}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if _, err := os.Stat(filepath.Join(candidate, "tokenizer.json")); err == nil {
			return candidate
		}
	}
	t.Skip("set GLINER_MODEL_DIR to the pinned GLiNER bundle to run parity tests")
	return ""
}

func hashInt64(values []int64) string {
	hash := sha256.New()
	var data [8]byte
	for _, value := range values {
		binary.LittleEndian.PutUint64(data[:], uint64(value))
		_, _ = hash.Write(data[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func hashBools(values []bool) string {
	data := make([]byte, len(values))
	for index, value := range values {
		if value {
			data[index] = 1
		}
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func TestTokenizerMatchesPinnedPythonReference(t *testing.T) {
	data, err := os.ReadFile("testdata/python_reference.json")
	require.NoError(t, err)
	var fixture tokenizerFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	tokenizer, err := loadTokenizer(referenceBundle(t))
	require.NoError(t, err)

	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			var text string
			if testCase.Text != nil {
				text = *testCase.Text
			} else {
				parts := make([]string, testCase.GeneratedText.Count)
				for index := range parts {
					parts[index] = testCase.GeneratedText.Prefix + strconv.Itoa(index)
				}
				text = strings.Join(parts, " ")
			}
			got, err := tokenizer.build(text, fixture.Labels)
			require.NoError(t, err)

			gotWords := make([]string, len(got.Words))
			gotOffsets := make([][]int, len(got.Words))
			for index, word := range got.Words {
				gotWords[index] = word.Text
				gotOffsets[index] = []int{word.RuneStart, word.RuneEnd}
			}
			assert.Equal(t, testCase.Words, gotWords)
			assert.Equal(t, testCase.WordOffsets, gotOffsets)
			assert.Equal(t, testCase.InputIDs, got.InputIDs)
			assert.Equal(t, testCase.AttentionMask, got.AttentionMask)
			assert.Equal(t, testCase.WordsMask, got.WordsMask)
			assert.Equal(t, testCase.TextLengths, got.TextLengths)
			assert.Equal(t, testCase.SpanIdxShape, []int{len(got.SpanMask), 2})
			assert.Equal(t, testCase.SpanIdxHash, hashInt64(got.SpanIdx))
			assert.Equal(t, testCase.SpanMaskShape, []int{len(got.SpanMask)})
			assert.Equal(t, testCase.SpanMaskHash, hashBools(got.SpanMask))
		})
	}
}
