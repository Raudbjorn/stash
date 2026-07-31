package ffmpeg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFFProbeTagsAllowlistAndCaseFolding(t *testing.T) {
	var probe FFProbeJSON
	err := json.Unmarshal([]byte(`{"format":{"tags":{
		"TITLE":"Container Title","Comment":"Container Comment","DESCRIPTION":"Description",
		"DATE":"2024-05-17","creation_time":"2024-05-17T10:30:00Z",
		"com.apple.quicktime.creationdate":"2024-05-17T10:30:00+0000",
		"ENCODER":"Lavf","artist":"must not persist"
	}}}`), &probe)
	require.NoError(t, err)
	assert.Equal(t, "Container Title", probe.Format.Tags.Title)
	assert.Equal(t, "Container Comment", probe.Format.Tags.Comment)
	assert.Equal(t, "Lavf", probe.Format.Tags.Encoder)
	assert.NotZero(t, probe.Format.Tags.CreationTime.Time)
	assert.Equal(t, map[string]string{
		"title": "Container Title", "comment": "Container Comment", "description": "Description",
		"date": "2024-05-17", "creation_time": "2024-05-17T10:30:00Z",
		"com.apple.quicktime.creationdate": "2024-05-17T10:30:00+0000", "encoder": "Lavf",
	}, probe.Format.Tags.Allowed)
}

func TestFFProbeTagsRejectOversizedValues(t *testing.T) {
	data, err := json.Marshal(map[string]any{"format": map[string]any{"tags": map[string]string{
		"title": strings.Repeat("x", maxProbeTagValueLength+1),
	}}})
	require.NoError(t, err)
	var probe FFProbeJSON
	require.NoError(t, json.Unmarshal(data, &probe))
	assert.Empty(t, probe.Format.Tags.Title)
	assert.Empty(t, probe.Format.Tags.Allowed)
}
