package metadata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseNFORecognizesAllowedFields(t *testing.T) {
	data, err := ParseNFO(strings.NewReader(`
<movie>
  <title> Primary Title </title><originaltitle>Fallback</originaltitle>
  <actor><name>Jane Doe</name></actor><actor><name>JANE.DOE</name></actor>
  <performer>Cher</performer><studio>Example Studio</studio>
  <premiered>2024-05-17</premiered><dateadded>2026-01-01</dateadded>
  <movie>Example Movie</movie><scene_number>05</scene_number>
  <plot>Ignored Person and Ignored Studio</plot>
</movie>`))
	require.NoError(t, err)
	assert.Equal(t, "Primary Title", data.Title)
	assert.Equal(t, []string{"Jane Doe", "Cher"}, data.Performers)
	assert.Equal(t, "Example Studio", data.Studio)
	assert.Equal(t, "2024-05-17", data.Date)
	assert.Equal(t, "Example Movie", data.Group)
	require.NotNil(t, data.SceneIndex)
	assert.Equal(t, 5, *data.SceneIndex)
}

func TestParseNFOFieldFallbacksAndDateAddedIgnored(t *testing.T) {
	data, err := ParseNFO(strings.NewReader(`<movie>
<originaltitle>Original</originaltitle><releasedate>2023-04-02</releasedate>
<group>Group Name</group><index>2</index><dateadded>2025-01-01</dateadded>
</movie>`))
	require.NoError(t, err)
	assert.Equal(t, "Original", data.Title)
	assert.Equal(t, "2023-04-02", data.Date)
	assert.Equal(t, "Group Name", data.Group)

	data, err = ParseNFO(strings.NewReader(`<movie><year>2020</year><set><name>Set Name</name></set></movie>`))
	require.NoError(t, err)
	assert.Equal(t, "2020", data.Date)
	assert.Equal(t, "Set Name", data.Group)

	data, err = ParseNFO(strings.NewReader(`<movie><dateadded>2025-01-01</dateadded></movie>`))
	require.NoError(t, err)
	assert.Empty(t, data.Date)
}

func TestParseNFORejectsMalformedOversizedAndDTD(t *testing.T) {
	_, err := ParseNFO(strings.NewReader(`<movie><title>broken</movie>`))
	assert.Error(t, err)
	_, err = ParseNFO(strings.NewReader(`<!DOCTYPE movie [<!ENTITY x "bad">]><movie>&x;</movie>`))
	assert.ErrorContains(t, err, "not allowed")
	_, err = ParseNFO(strings.NewReader(strings.Repeat("x", int(MaxNFOSize)+1)))
	assert.ErrorContains(t, err, "exceeds")
}

func TestReadAdjacentNFOOrderingAndSoleVideoFallback(t *testing.T) {
	directory := t.TempDir()
	video := filepath.Join(directory, "Scene.MP4")
	require.NoError(t, os.WriteFile(video, nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "movie.nfo"), []byte(`<movie><title>Movie Fallback</title></movie>`), 0o600))

	got, err := ReadAdjacentNFO(video)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "Movie Fallback", got.Title)

	require.NoError(t, os.WriteFile(filepath.Join(directory, "Scene.nfo"), []byte(`<movie><title>Stem First</title></movie>`), 0o600))
	got, err = ReadAdjacentNFO(video)
	require.NoError(t, err)
	assert.Equal(t, "Stem First", got.Title)

	require.NoError(t, os.WriteFile(filepath.Join(directory, "other.mkv"), nil, 0o600))
	require.NoError(t, os.Remove(filepath.Join(directory, "Scene.nfo")))
	got, err = ReadAdjacentNFO(video)
	require.NoError(t, err)
	assert.Nil(t, got, "movie.nfo must not be used in a multi-video directory")
}

func TestReadAdjacentNFOIsCaseSensitiveAndMissingIsNormal(t *testing.T) {
	directory := t.TempDir()
	video := filepath.Join(directory, "Scene.mp4")
	require.NoError(t, os.WriteFile(video, nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "Scene.NFO"), []byte(`<movie/>`), 0o600))
	got, err := ReadAdjacentNFO(video)
	require.NoError(t, err)
	assert.Nil(t, got)
}
