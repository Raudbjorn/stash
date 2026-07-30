package gliner

import (
	"context"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	ort "github.com/yalue/onnxruntime_go"
)

func TestSplitSourceWordsPreservesUTF8ByteOffsetsAndLimit(t *testing.T) {
	text := "  Jane\tDœ  李雷 "
	got := splitSourceWords(text, maxWords)
	wantText := []string{"Jane", "Dœ", "李雷"}
	if len(got) != len(wantText) {
		t.Fatalf("splitSourceWords() = %#v", got)
	}
	for i, word := range got {
		if word.text != wantText[i] || text[word.start:word.end] != wantText[i] {
			t.Errorf("word %d = %#v, source slice %q", i, word, text[word.start:word.end])
		}
	}

	many := strings.Repeat("word ", maxWords+10)
	if count := len(splitSourceWords(many, maxWords)); count != maxWords {
		t.Fatalf("word count = %d, want %d", count, maxWords)
	}
}

func TestPrepareSpanIndicesUsesInclusiveEnds(t *testing.T) {
	indices, mask := prepareSpanIndices(2, 3)
	wantIndices := []int64{0, 0, 0, 1, 0, 2, 1, 1, 1, 2, 1, 3}
	wantMask := []bool{true, true, false, true, false, false}
	if !reflect.DeepEqual(indices, wantIndices) || !reflect.DeepEqual(mask, wantMask) {
		t.Fatalf("prepareSpanIndices() = (%v, %v), want (%v, %v)", indices, mask, wantIndices, wantMask)
	}
}

func TestDecodeSpansThresholdOverlapAndByteOffsets(t *testing.T) {
	text := "Jane Doe met Acmé Studio"
	words := splitSourceWords(text, maxWords)
	_, mask := prepareSpanIndices(len(words), maxSpanWidth)
	labels := []string{"person", "production studio", "scene title"}
	logits := make([]float32, len(mask)*len(labels))
	for i := range logits {
		logits[i] = -20
	}
	setLogit := func(start, width, label int, value float32) {
		span := start*maxSpanWidth + width
		logits[span*len(labels)+label] = value
	}
	setLogit(0, 1, 0, 4) // Jane Doe; wins same-label overlap.
	setLogit(0, 0, 0, 3) // Jane; suppressed by Jane Doe.
	setLogit(0, 1, 2, 3) // Different kind may overlap.
	setLogit(3, 1, 1, 4) // Acmé Studio; exercises UTF-8 byte slicing.
	setLogit(2, 0, 0, 0) // sigmoid(0) is below the 0.6 threshold.

	got, err := decodeSpans(text, words, labels, 0.6, mask, ort.Shape{1, int64(len(mask)), int64(len(labels))}, logits, 3)
	if err != nil {
		t.Fatalf("decodeSpans() error = %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("decodeSpans() = %#v, want 3 spans", got)
	}
	want := []struct{ text, label string }{
		{"Jane Doe", "person"},
		{"Jane Doe", "scene title"},
		{"Acmé Studio", "production studio"},
	}
	for i, span := range got {
		if span.Text != want[i].text || span.Label != want[i].label || text[span.Start:span.End] != span.Text {
			t.Errorf("span %d = %#v, want text=%q label=%q", i, span, want[i].text, want[i].label)
		}
		if span.Score < float32(1/(1+math.Exp(-3))) {
			t.Errorf("span %d score = %v", i, span.Score)
		}
	}
}

func TestDecodeSpansRejectsWrongShape(t *testing.T) {
	words := splitSourceWords("Jane Doe", maxWords)
	_, mask := prepareSpanIndices(len(words), maxSpanWidth)
	_, err := decodeSpans("Jane Doe", words, []string{"person"}, 0.5, mask, ort.Shape{1, 1, 1}, make([]float32, len(mask)), 3)
	if err == nil {
		t.Fatal("decodeSpans() error = nil, want shape error")
	}
}

func TestPinnedModelIntegration(t *testing.T) {
	modelPath := os.Getenv("STASH_SCENE_METADATA_MODEL_PATH")
	libraryPath := os.Getenv("ONNXRUNTIME_LIB_PATH")
	if modelPath == "" || libraryPath == "" {
		t.Skip("set STASH_SCENE_METADATA_MODEL_PATH and ONNXRUNTIME_LIB_PATH to run pinned GLiNER integration")
	}
	ort.SetSharedLibraryPath(libraryPath)
	if err := ort.InitializeEnvironment(); err != nil {
		t.Fatalf("InitializeEnvironment() error = %v", err)
	}
	t.Cleanup(func() { _ = ort.DestroyEnvironment() })

	model, err := Load(modelPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(func() { _ = model.Close() })

	for _, text := range []string{
		"Kenzie Reeves at Example Studio for Sample Movie Scene 2 on 2024-07-15",
		"João Silva no Estúdio Exemplo para Filme Amostra cena 3 em 2024-07-15",
	} {
		spans, err := model.Extract(context.Background(), text, ProductionLabels, 0.05)
		if err != nil {
			t.Fatalf("Extract(%q) error = %v", text, err)
		}
		if len(spans) == 0 {
			t.Fatalf("Extract(%q) returned no spans", text)
		}
		for _, span := range spans {
			if span.Start < 0 || span.End > len(text) || span.Start >= span.End || text[span.Start:span.End] != span.Text {
				t.Fatalf("invalid source span %#v for %q", span, text)
			}
		}
	}
}
