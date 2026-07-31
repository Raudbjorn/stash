package metadata

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectSourcesPreservesProvenanceAndOrder(t *testing.T) {
	sources := CollectSources(SourceInputs{
		FilenameStem: "Example.Movie.Scene.5",
		SceneTitle:   "Current Title",
		Details:      "Details",
		UseDetails:   true,
		NFO: &NFOData{
			Title: "NFO Title", Performers: []string{"Jane Doe", "Cher"}, Studio: "Example Studio",
			Date: "2024-05-17", Group: "Example Movie",
		},
		Container: ContainerMetadata{
			Title: "Container Title", Comment: "Comment", Encoder: "Lavf",
			Tags: map[string]string{"date": "2024-05-17", "description": "Description"},
		},
	})
	require.Len(t, sources, 14)
	assert.Equal(t, SourceFilename, sources[0].Kind)
	assert.Equal(t, "example movie scene 5", sources[0].Normalized)
	assert.Equal(t, "nfo performer", sources[4].Label)
	assert.Equal(t, TrustExplicit, sources[4].Trust)
	assert.Equal(t, "container tag date", sources[11].Label)
	assert.Equal(t, "container tag description", sources[12].Label)
	assert.Equal(t, "container encoder", sources[13].Label)
}

func TestCollectSourcesBoundsUnicodeWithoutCorruption(t *testing.T) {
	sources := CollectSources(SourceInputs{FilenameStem: strings.Repeat("é", MaxSourceBytes)})
	require.Len(t, sources, 1)
	assert.LessOrEqual(t, len(sources[0].RawText), MaxSourceBytes)
	assert.NotContains(t, sources[0].RawText, "�")
}

func TestCollectSourcesOmitsDetailsByDefault(t *testing.T) {
	sources := CollectSources(SourceInputs{FilenameStem: "File", Details: "Private details"})
	require.Len(t, sources, 1)
	assert.Equal(t, SourceFilename, sources[0].Kind)
}
