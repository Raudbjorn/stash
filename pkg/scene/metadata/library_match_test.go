package metadata

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFindExactNamedSpansUsesLexicalBoundariesAndAliases(t *testing.T) {
	source := Source{Kind: SourceFilename, Label: "primary filename", RawText: "Jane.Doe_and_María-O’Neil_NotJane Doe"}
	records := []NamedAliases{
		{ID: 1, Name: "Jane Doe"},
		{ID: 2, Name: "Maria Oneil", Aliases: []string{"María O’Neil"}},
	}
	spans := FindExactNamedSpans(source, records, EntityLabelPerformer)
	assert.Len(t, spans, 2)
	assert.Equal(t, "Jane.Doe", spans[0].Text)
	assert.Equal(t, 1, *spans[0].EntityID)
	assert.Equal(t, "María-O’Neil", spans[1].Text)
	assert.Equal(t, 2, *spans[1].EntityID)
}

func TestFindExactNamedSpansRejectsPartialAndAmbiguousAliases(t *testing.T) {
	source := Source{Kind: SourceFilename, RawText: "Ann Shared Alias"}
	records := []NamedAliases{
		{ID: 1, Name: "Anna", Aliases: []string{"Shared Alias"}},
		{ID: 2, Name: "Anne", Aliases: []string{"Shared_Alias"}},
	}
	assert.Empty(t, FindExactNamedSpans(source, records, EntityLabelPerformer))
}

func TestExactIndexPrefersLongestOverlappingPhrase(t *testing.T) {
	index := BuildExactIndex([]NamedAliases{
		{ID: 1, Name: "Jane"},
		{ID: 2, Name: "Jane Doe"},
	})

	spans := index.Scan(Source{Kind: SourceSceneTitle, RawText: "Jane Doe"}, EntityLabelPerformer)
	if assert.Len(t, spans, 1) {
		assert.Equal(t, "Jane Doe", spans[0].Text)
		assert.Equal(t, 2, *spans[0].EntityID)
	}
}

func BenchmarkExactIndex_Scan(b *testing.B) {
	const (
		recordCount = 10_000
		sourceCount = 1_000
	)
	records := make([]NamedAliases, recordCount)
	sources := make([]Source, sourceCount)
	for recordIndex := range records {
		records[recordIndex] = NamedAliases{
			ID:   recordIndex + 1,
			Name: "performer " + strconv.Itoa(recordIndex),
		}
	}
	for sourceIndex := range sources {
		sources[sourceIndex] = Source{
			Kind:    SourceFilename,
			RawText: "release performer " + strconv.Itoa(sourceIndex) + " final",
		}
	}
	index := BuildExactIndex(records)

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, source := range sources {
			index.Scan(source, EntityLabelPerformer)
		}
	}
	b.ReportMetric(sourceCount, "sources/op")
}
