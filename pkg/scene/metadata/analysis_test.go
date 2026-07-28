package metadata

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingExtractor struct {
	calls []string
	spans []EntitySpan
}

func (e *recordingExtractor) Extract(_ context.Context, text string, _ []string) ([]EntitySpan, error) {
	e.calls = append(e.calls, text)
	return append([]EntitySpan(nil), e.spans...), nil
}

func TestNormalizeKeyPreservesNamesAndFoldsSeparators(t *testing.T) {
	assert.Equal(t, "maría o’neil élodie d’ambrosio", NormalizeKey("  María.O’Neil__ÉLODIE--D’Ambrosio "))
	assert.Equal(t, "佐藤 miyuki", NormalizeKey("佐藤_Miyuki"))
}

func TestResolveOverlappingSpansPrecedence(t *testing.T) {
	source := Source{Kind: SourceFilename, Label: "file"}
	spans := []Span{
		{Source: source, ByteStart: 0, ByteEnd: 8, Text: "Jane Doe", Score: .99, Kind: EvidenceGLiNERSpan},
		{Source: source, ByteStart: 0, ByteEnd: 13, Text: "Jane Doe Smith", Score: .80, Kind: EvidenceGLiNERSpan},
		{Source: source, ByteStart: 5, ByteEnd: 13, Text: "Doe Smith", Score: .10, Kind: EvidenceExplicitField},
	}

	got := ResolveOverlappingSpans(spans)
	require.Len(t, got, 1)
	assert.Equal(t, "Doe Smith", got[0].Text, "explicit evidence must outrank model confidence")
}

func TestResolveOverlappingSpansConfidenceThenLength(t *testing.T) {
	source := Source{Kind: SourceFilename, Label: "file"}
	spans := []Span{
		{Source: source, ByteStart: 0, ByteEnd: 8, Text: "Jane Doe", Score: .8, Kind: EvidenceGLiNERSpan},
		{Source: source, ByteStart: 0, ByteEnd: 13, Text: "Jane Doe Smith", Score: .8, Kind: EvidenceGLiNERSpan},
	}

	got := ResolveOverlappingSpans(spans)
	require.Len(t, got, 1)
	assert.Equal(t, "Jane Doe Smith", got[0].Text)
}

func TestMergeCandidatesCountsUniqueNormalizedValues(t *testing.T) {
	spans := []Span{
		{Source: Source{Kind: SourceFilename}, Text: "María O’Neil", Score: .7},
		{Source: Source{Kind: SourceNFO}, Text: "MARÍA.O’NEIL", Score: 1},
		{Source: Source{Kind: SourceSceneTitle}, Text: "Jane Doe", Score: .8},
	}

	got := MergeCandidates(spans)
	require.Len(t, got, 2)
	assert.Equal(t, 2, got[0].OccurrenceCount)
	assert.Equal(t, 2, got[0].EvidenceCount)
	assert.ElementsMatch(t, []SourceKind{SourceFilename, SourceNFO}, got[0].Sources)
	assert.Equal(t, 1.0, got[0].Confidence)
}

func TestAnalyzerInvokesExtractorOncePerNormalizedSource(t *testing.T) {
	extractor := &recordingExtractor{spans: []EntitySpan{{
		ByteStart: 0, ByteEnd: 8, Text: "Jane Doe", Label: "adult performer name", Score: .9,
	}}}
	analysis, err := (Analyzer{}).Analyze(context.Background(), Inputs{
		Sources: []Source{
			{Kind: SourceFilename, RawText: "Jane.Doe"},
			{Kind: SourceSceneTitle, RawText: "JANE_DOE"},
		},
		EntityExtractor: extractor,
	})
	require.NoError(t, err)
	assert.Len(t, extractor.calls, 1)
	assert.Equal(t, 1, analysis.UniquePotentialPerformers)
	assert.Equal(t, 1, analysis.Diagnostics.RawPerformerSpanCount)
}

func TestCorpusCoversRequiredCases(t *testing.T) {
	data, err := os.ReadFile("testdata/corpus.json")
	require.NoError(t, err)
	var corpus []map[string]any
	require.NoError(t, json.Unmarshal(data, &corpus))
	require.GreaterOrEqual(t, len(corpus), 8)

	var hasUnicode, hasOneWord, hasMultiple, hasScene, hasTVFalsePositive bool
	for _, fixture := range corpus {
		name, _ := fixture["name"].(string)
		switch name {
		case "unicode and mixed scripts":
			hasUnicode = true
		case "one word stage name from explicit NFO":
			hasOneWord = true
		case "multiple performers and technical suffix":
			hasMultiple = true
		case "explicit movie scene marker":
			hasScene = true
		case "tv marker and bare number are not scenes":
			hasTVFalsePositive = true
		}
	}
	assert.True(t, hasUnicode && hasOneWord && hasMultiple && hasScene && hasTVFalsePositive)
}
func BenchmarkAnalyzeDeterministic(b *testing.B) {
	inputs := Inputs{Sources: CollectSources(SourceInputs{
		FilenameStem: "ExampleStudio.2024.05.17.Jane.Doe.Maria.O'Neill.City.Nights.2160p.WEB-DL.DDP5.1.HEVC-GROUP",
		SceneTitle:   "ExampleStudio Jane Doe Maria O'Neill City Nights 2160p",
	})}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := (Analyzer{}).Analyze(ctx, inputs); err != nil {
			b.Fatal(err)
		}
	}
}
