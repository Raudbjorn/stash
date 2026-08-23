package metadata

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseTextMetadataTechnicalDateAndReleaseGroup(t *testing.T) {
	text := "ExampleStudio.2024.05.17.Jane.Doe.City.Nights.2160p.WEB-DL.DDP5.1.HEVC-GROUP.mkv"
	parsed := ParseTextMetadata(text)
	require.NotNil(t, parsed.ReleaseGroup)
	assert.Equal(t, "GROUP", parsed.ReleaseGroup.Text)
	require.Len(t, parsed.DateSpans, 1)
	assert.Equal(t, "2024.05.17", parsed.DateSpans[0].Text)
	technical := make([]string, 0, len(parsed.TechnicalSpans))
	for _, span := range parsed.TechnicalSpans {
		technical = append(technical, span.Text)
	}
	assert.ElementsMatch(t, []string{"2160p", "WEB-DL", "DDP5.1", "HEVC", "mkv", ".mkv"}, technical)

	xvid := ParseTextMetadata("23 02 12 And The Handjob XviD-iPT")
	require.NotNil(t, xvid.ReleaseGroup)
	assert.Equal(t, "iPT", xvid.ReleaseGroup.Text)
	require.Len(t, xvid.DateSpans, 1)
	assert.Equal(t, "23 02 12", xvid.DateSpans[0].Text)
	xvidTechnical := make([]string, 0, len(xvid.TechnicalSpans))
	for _, span := range xvid.TechnicalSpans {
		xvidTechnical = append(xvidTechnical, span.Text)
	}
	assert.Contains(t, xvidTechnical, "XviD")
}

func TestParseTextMetadataExplicitSceneMarkersOnly(t *testing.T) {
	for _, tc := range []struct {
		text  string
		index int
	}{
		{"Example Movie - Scene 5 - Jane Doe", 5},
		{"Example Movie.Sc 05.Jane Doe", 5},
		{"Title S01E01 2024 7", 0},
		{"Title Scene 0", 0},
	} {
		t.Run(tc.text, func(t *testing.T) {
			got := ParseTextMetadata(tc.text).SceneMarker
			if tc.index == 0 {
				assert.Nil(t, got)
			} else if assert.NotNil(t, got) {
				assert.Equal(t, tc.index, got.Index)
			}
		})
	}
}

func TestCleanTitlePreservesUnknownAndOrdinaryTokens(t *testing.T) {
	assert.Equal(t, "A-Normal-Hyphenated-Title S01E01 2024 7", CleanTitle("A-Normal-Hyphenated-Title.S01E01.2024.1080p.7.mkv", nil))
	assert.Equal(t, "Jane Doe Ordinary Title", CleanTitle("Jane Doe - Ordinary Title [1080p x264 DirectorsCut].mkv", nil))
	assert.Equal(t, "Ordinary Title", CleanTitle("Ordinary.Title.[1080p.x264].mkv", nil))
	assert.Equal(t, "PervMom", CleanTitle("[XXXClub to]PervMom MP4- [XC]", nil))
	assert.Equal(t, "And The Handjob", CleanTitle("23 02 12 And The Handjob XviD-iPT", nil))
}

func TestCleanTitleRemovesRecognizedEntitySpans(t *testing.T) {
	text := "Example Studio - Example Movie - Scene 5 - Jane Doe - City Nights - 1080p.mkv"
	recognized := []EntitySpan{
		spanFor(t, text, "Example Studio", EntityLabelStudio),
		spanFor(t, text, "Example Movie", EntityLabelMovie),
		spanFor(t, text, "Jane Doe", EntityLabelPerformer),
	}
	assert.Equal(t, "City Nights", CleanTitle(text, recognized))
}

func TestInvalidCalendarDateIsNotRemoved(t *testing.T) {
	text := "Title.2024.02.31.1080p.mkv"
	assert.Empty(t, ParseTextMetadata(text).DateSpans)
	assert.Equal(t, "Title 2024 02 31", CleanTitle(text, nil))
}

func spanFor(t *testing.T, text, target, label string) EntitySpan {
	t.Helper()
	start := -1
	for i := 0; i+len(target) <= len(text); i++ {
		if text[i:i+len(target)] == target {
			start = i
			break
		}
	}
	require.NotEqual(t, -1, start)
	return EntitySpan{ByteStart: start, ByteEnd: start + len(target), Text: target, Label: label, Score: 1}
}
