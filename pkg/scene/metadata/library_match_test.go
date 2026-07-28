package metadata

import (
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
