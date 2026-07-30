package ffmpeg

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestParsePreservesArbitraryFormatAndStreamTags(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "video-*.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("fixture")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	fixture := []byte(`{
		"format": {
			"format_name": "mov,mp4",
			"duration": "12.5",
			"bit_rate": "1000",
			"tags": {
				"TITLE": "Canonical Title",
				"comment": "Description",
				"publisher": "Example Studio",
				"creation_time": "2024-07-15T10:11:12Z",
				"show": "Sample Movie",
				"episode_id": "2",
				"custom-key": "custom-value"
			}
		},
		"streams": [{
			"codec_type": "video",
			"codec_name": "h264",
			"duration": "12.5",
			"avg_frame_rate": "30/1",
			"width": 1920,
			"height": 1080,
			"tags": {"ROTATE": "90", "handler_name": "VideoHandler"}
		}]
	}`)
	var probe FFProbeJSON
	if err := json.Unmarshal(fixture, &probe); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	got, err := parse(file.Name(), &probe)
	if err != nil {
		t.Fatalf("parse() error = %v", err)
	}
	if got.Title != "Canonical Title" || got.Comment != "Description" {
		t.Errorf("legacy fields = title %q comment %q", got.Title, got.Comment)
	}
	if got.FormatTags.Get("PUBLISHER") != "Example Studio" || got.FormatTags.Get("custom-key") != "custom-value" {
		t.Errorf("format tags = %#v", got.FormatTags)
	}
	if len(got.StreamTags) != 1 || got.StreamTags[0].Get("handler_name") != "VideoHandler" {
		t.Errorf("stream tags = %#v", got.StreamTags)
	}
	if got.Width != 1080 || got.Height != 1920 {
		t.Errorf("rotated dimensions = %dx%d, want 1080x1920", got.Width, got.Height)
	}
	wantCreationTime := time.Date(2024, 7, 15, 10, 11, 12, 0, time.UTC)
	if !got.CreationTime.Equal(wantCreationTime) {
		t.Errorf("creation time = %v, want %v", got.CreationTime, wantCreationTime)
	}
}
