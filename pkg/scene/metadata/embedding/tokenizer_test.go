package embedding

import (
	"strings"
	"testing"
)

// testVocab is a tiny hand-built vocab covering exactly what the test cases
// below need, in the standard BERT id-by-line-number format.
var testVocab = []string{
	"[PAD]", "[UNK]", "[CLS]", "[SEP]",
	"jane", "doe", "john", "smith",
	"scene", "video",
	"perform", "##er", "##s",
	".", "-",
}

func newTestTokenizer(t *testing.T) *Tokenizer {
	t.Helper()
	tok, err := NewTokenizer(strings.NewReader(strings.Join(testVocab, "\n")))
	if err != nil {
		t.Fatalf("NewTokenizer: %v", err)
	}
	return tok
}

func idOf(t *testing.T, token string) int64 {
	t.Helper()
	for i, v := range testVocab {
		if v == token {
			return int64(i)
		}
	}
	t.Fatalf("token %q not in testVocab", token)
	return -1
}

func TestEncode_SimpleName(t *testing.T) {
	tok := newTestTokenizer(t)

	inputIDs, attentionMask, tokenTypeIDs := tok.Encode("Jane Doe", 8)

	want := []int64{
		idOf(t, "[CLS]"), idOf(t, "jane"), idOf(t, "doe"), idOf(t, "[SEP]"),
		0, 0, 0, 0,
	}
	if len(inputIDs) != len(want) {
		t.Fatalf("input_ids length = %d, want %d", len(inputIDs), len(want))
	}
	for i := range want {
		if inputIDs[i] != want[i] {
			t.Errorf("input_ids[%d] = %d, want %d", i, inputIDs[i], want[i])
		}
	}

	wantMask := []int64{1, 1, 1, 1, 0, 0, 0, 0}
	for i := range wantMask {
		if attentionMask[i] != wantMask[i] {
			t.Errorf("attention_mask[%d] = %d, want %d", i, attentionMask[i], wantMask[i])
		}
	}

	for i, v := range tokenTypeIDs {
		if v != 0 {
			t.Errorf("token_type_ids[%d] = %d, want 0", i, v)
		}
	}
}

func TestEncode_WordpieceSplitsUnknownSuffix(t *testing.T) {
	tok := newTestTokenizer(t)

	inputIDs, attentionMask, _ := tok.Encode("performers", 8)

	want := []int64{
		idOf(t, "[CLS]"), idOf(t, "perform"), idOf(t, "##er"), idOf(t, "##s"), idOf(t, "[SEP]"),
		0, 0, 0,
	}
	for i := range want {
		if inputIDs[i] != want[i] {
			t.Errorf("input_ids[%d] = %d, want %d", i, inputIDs[i], want[i])
		}
	}
	wantMask := []int64{1, 1, 1, 1, 1, 0, 0, 0}
	for i := range wantMask {
		if attentionMask[i] != wantMask[i] {
			t.Errorf("attention_mask[%d] = %d, want %d", i, attentionMask[i], wantMask[i])
		}
	}
}

func TestEncode_UnknownWordFallsBackToUNK(t *testing.T) {
	tok := newTestTokenizer(t)

	inputIDs, _, _ := tok.Encode("xyzzy", 8)

	if inputIDs[1] != idOf(t, "[UNK]") {
		t.Errorf("input_ids[1] = %d, want [UNK] (%d)", inputIDs[1], idOf(t, "[UNK]"))
	}
}

func TestEncode_PunctuationSplitIntoOwnTokens(t *testing.T) {
	tok := newTestTokenizer(t)

	inputIDs, _, _ := tok.Encode("jane-doe.", 10)

	want := []int64{
		idOf(t, "[CLS]"), idOf(t, "jane"), idOf(t, "-"), idOf(t, "doe"), idOf(t, "."), idOf(t, "[SEP]"),
	}
	for i := range want {
		if inputIDs[i] != want[i] {
			t.Errorf("input_ids[%d] = %d, want %d", i, inputIDs[i], want[i])
		}
	}
}

func TestEncode_TruncatesToMaxLen(t *testing.T) {
	tok := newTestTokenizer(t)

	inputIDs, attentionMask, _ := tok.Encode("jane doe john smith", 5)

	if len(inputIDs) != 5 {
		t.Fatalf("input_ids length = %d, want 5", len(inputIDs))
	}
	// [CLS] + 3 pieces (truncated from 4) + [SEP] == 5 slots, all attended.
	if inputIDs[4] != idOf(t, "[SEP]") {
		t.Errorf("last token = %d, want [SEP] (%d)", inputIDs[4], idOf(t, "[SEP]"))
	}
	for i, v := range attentionMask {
		if v != 1 {
			t.Errorf("attention_mask[%d] = %d, want 1 (no padding expected)", i, v)
		}
	}
}

func TestNewTokenizer_MissingSpecialToken(t *testing.T) {
	_, err := NewTokenizer(strings.NewReader("hello\nworld\n"))
	if err == nil {
		t.Fatal("expected error for vocab missing special tokens, got nil")
	}
}
