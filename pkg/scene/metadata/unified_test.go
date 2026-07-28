package metadata

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnalyzerMergesExplicitDeterministicEvidenceWithoutModel(t *testing.T) {
	sources := CollectSources(SourceInputs{
		FilenameStem: "Example Movie - Scene 5 - Jane Doe - City Nights - 1080p",
		NFO: &NFOData{
			Title: "City Nights", Performers: []string{"Jane Doe"}, Studio: "Example Studio",
			Date: "2024-05-17", Group: "Example Movie",
		},
	})
	analysis, err := (Analyzer{}).Analyze(context.Background(), Inputs{
		Sources: sources, SanityBound: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	assert.False(t, analysis.Diagnostics.ModelAvailable)
	assert.Equal(t, 1, analysis.UniquePotentialPerformers)
	require.Len(t, analysis.PerformerCandidates, 1)
	assert.Equal(t, "Jane Doe", analysis.PerformerCandidates[0].Value)
	require.NotNil(t, analysis.StudioCandidate)
	assert.Equal(t, "Example Studio", analysis.StudioCandidate.Value)
	require.NotNil(t, analysis.Date)
	assert.Equal(t, "2024-05-17", analysis.Date.Date.Format("2006-01-02"))
	require.NotNil(t, analysis.Group)
	assert.Equal(t, "Example Movie", analysis.Group.Name)
	require.NotNil(t, analysis.Group.SceneIndex)
	assert.Equal(t, 5, *analysis.Group.SceneIndex)
	require.NotNil(t, analysis.Title)
	assert.Equal(t, "City Nights", analysis.Title.Value)
}

func TestAnalyzerCarriesExactLibraryIDs(t *testing.T) {
	id := 42
	source := NormalizeSource(Source{Kind: SourceFilename, Label: "primary filename", RawText: "Jane Doe Scene"})
	analysis, err := (Analyzer{}).Analyze(context.Background(), Inputs{
		Sources: []Source{source},
		AdditionalSpans: []Span{{
			Source: source, ByteStart: 0, ByteEnd: 8, RuneStart: 0, RuneEnd: 8,
			Text: "Jane Doe", NormalizedKey: "jane doe", Label: EntityLabelPerformer,
			Score: 1, Kind: EvidenceExactLibraryMatch, EntityID: &id,
		}},
	})
	require.NoError(t, err)
	assert.Equal(t, []int{42}, analysis.MatchedPerformerIDs)
	require.Len(t, analysis.PerformerCandidates, 1)
	assert.Equal(t, &id, analysis.PerformerCandidates[0].ExistingEntityID)
}

func TestAnalyzerUsesExactGroupMarkerAndSceneTitleFallback(t *testing.T) {
	source := NormalizeSource(Source{
		Kind: SourceSceneTitle, Label: "scene title",
		RawText: "Smoke Studio Smoke Jane Smoke Movie Scene 5 City Nights 2024-05-17 1080p",
	})
	var exact []Span
	exact = append(exact, FindExactNamedSpans(source, []NamedAliases{{ID: 1, Name: "Smoke Jane"}}, EntityLabelPerformer)...)
	exact = append(exact, FindExactNamedSpans(source, []NamedAliases{{ID: 2, Name: "Smoke Studio"}}, EntityLabelStudio)...)
	exact = append(exact, FindExactNamedSpans(source, []NamedAliases{{ID: 3, Name: "Smoke Movie"}}, EntityLabelMovie)...)

	analysis, err := (Analyzer{}).Analyze(context.Background(), Inputs{
		Sources: []Source{source}, AdditionalSpans: exact, EntityExtractor: &recordingExtractor{},
		SanityBound: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	require.NoError(t, err)
	require.NotNil(t, analysis.Group)
	assert.Equal(t, 3, *analysis.Group.ExistingID)
	assert.Equal(t, 5, *analysis.Group.SceneIndex)
	require.NotNil(t, analysis.Title)
	assert.Equal(t, "City Nights", analysis.Title.Value)
}

func TestAnalyzerFallbackDoesNotRemoveHeuristicTitleWords(t *testing.T) {
	analysis, err := (Analyzer{}).Analyze(context.Background(), Inputs{
		Sources: CollectSources(SourceInputs{FilenameStem: "Ordinary Title 1080p"}),
	})
	require.NoError(t, err)
	require.NotNil(t, analysis.Title)
	assert.Equal(t, "Ordinary Title", analysis.Title.Value)
}

func TestAnalyzerDoesNotResolveGroupWithoutExplicitMarker(t *testing.T) {
	analysis, err := (Analyzer{}).Analyze(context.Background(), Inputs{Sources: CollectSources(SourceInputs{
		FilenameStem: "Example Movie S01E01 1080p", NFO: &NFOData{Group: "Example Movie"},
	})})
	require.NoError(t, err)
	assert.Nil(t, analysis.Group)
	assert.Nil(t, analysis.Technical.SceneIndex)
}
