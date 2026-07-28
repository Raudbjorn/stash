package ffmpeg

import (
	stdjson "encoding/json"
	"strings"
	"unicode/utf8"

	modeljson "github.com/stashapp/stash/pkg/models/json"
)

// FFProbeJSON is the JSON output of ffprobe.
type FFProbeJSON struct {
	Format struct {
		BitRate        string      `json:"bit_rate"`
		Duration       string      `json:"duration"`
		Filename       string      `json:"filename"`
		FormatLongName string      `json:"format_long_name"`
		FormatName     string      `json:"format_name"`
		NbPrograms     int         `json:"nb_programs"`
		NbStreams      int         `json:"nb_streams"`
		ProbeScore     int         `json:"probe_score"`
		Size           string      `json:"size"`
		StartTime      string      `json:"start_time"`
		Tags           FFProbeTags `json:"tags"`
	} `json:"format"`
	Streams []FFProbeStream `json:"streams"`
	Error   struct {
		Code   int    `json:"code"`
		String string `json:"string"`
	} `json:"error"`
}

const (
	maxProbeTagKeyLength   = 128
	maxProbeTagValueLength = 4096
)

var allowedProbeTags = map[string]struct{}{
	"title": {}, "comment": {}, "description": {}, "date": {}, "creation_time": {},
	"com.apple.quicktime.creationdate": {}, "encoder": {},
}

type FFProbeTags struct {
	CreationTime modeljson.JSONTime
	Title        string
	Comment      string
	Encoder      string
	Allowed      map[string]string
}

func (t *FFProbeTags) UnmarshalJSON(data []byte) error {
	var raw map[string]stdjson.RawMessage
	if err := stdjson.Unmarshal(data, &raw); err != nil {
		return err
	}
	t.Allowed = make(map[string]string, len(raw))
	for originalKey, encoded := range raw {
		key := strings.ToLower(strings.TrimSpace(originalKey))
		if len(key) == 0 || len(key) > maxProbeTagKeyLength {
			continue
		}
		if _, ok := allowedProbeTags[key]; !ok {
			continue
		}
		var value string
		if err := stdjson.Unmarshal(encoded, &value); err != nil {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || len(value) > maxProbeTagValueLength || !utf8.ValidString(value) {
			continue
		}
		t.Allowed[key] = value
		switch key {
		case "title":
			t.Title = value
		case "comment":
			t.Comment = value
		case "encoder":
			t.Encoder = value
		case "creation_time":
			if err := stdjson.Unmarshal(encoded, &t.CreationTime); err != nil {
				t.CreationTime = modeljson.JSONTime{}
			}
		}
	}
	return nil
}

// FFProbeStream is a JSON representation of an ffmpeg stream.
type FFProbeStream struct {
	AvgFrameRate       string `json:"avg_frame_rate"`
	BitRate            string `json:"bit_rate"`
	BitsPerRawSample   string `json:"bits_per_raw_sample,omitempty"`
	ChromaLocation     string `json:"chroma_location,omitempty"`
	CodecLongName      string `json:"codec_long_name"`
	CodecName          string `json:"codec_name"`
	CodecTag           string `json:"codec_tag"`
	CodecTagString     string `json:"codec_tag_string"`
	CodecTimeBase      string `json:"codec_time_base"`
	CodecType          string `json:"codec_type"`
	CodedHeight        int    `json:"coded_height,omitempty"`
	CodedWidth         int    `json:"coded_width,omitempty"`
	DisplayAspectRatio string `json:"display_aspect_ratio,omitempty"`
	Disposition        struct {
		AttachedPic     int `json:"attached_pic"`
		CleanEffects    int `json:"clean_effects"`
		Comment         int `json:"comment"`
		Default         int `json:"default"`
		Dub             int `json:"dub"`
		Forced          int `json:"forced"`
		HearingImpaired int `json:"hearing_impaired"`
		Karaoke         int `json:"karaoke"`
		Lyrics          int `json:"lyrics"`
		Original        int `json:"original"`
		TimedThumbnails int `json:"timed_thumbnails"`
		VisualImpaired  int `json:"visual_impaired"`
	} `json:"disposition"`
	Duration          string `json:"duration"`
	DurationTs        int64  `json:"duration_ts"`
	HasBFrames        int    `json:"has_b_frames,omitempty"`
	Height            int    `json:"height,omitempty"`
	Index             int    `json:"index"`
	IsAvc             string `json:"is_avc,omitempty"`
	Level             int    `json:"level,omitempty"`
	NalLengthSize     string `json:"nal_length_size,omitempty"`
	NbFrames          string `json:"nb_frames"`
	NbReadFrames      string `json:"nb_read_frames"`
	PixFmt            string `json:"pix_fmt,omitempty"`
	Profile           string `json:"profile"`
	RFrameRate        string `json:"r_frame_rate"`
	Refs              int    `json:"refs,omitempty"`
	SampleAspectRatio string `json:"sample_aspect_ratio,omitempty"`
	StartPts          int64  `json:"start_pts"`
	StartTime         string `json:"start_time"`
	Tags              struct {
		CreationTime modeljson.JSONTime `json:"creation_time"`
		HandlerName  string             `json:"handler_name"`
		Language     string             `json:"language"`
		Rotate       string             `json:"rotate"`
	} `json:"tags"`
	TimeBase      string `json:"time_base"`
	Width         int    `json:"width,omitempty"`
	BitsPerSample int    `json:"bits_per_sample,omitempty"`
	ChannelLayout string `json:"channel_layout,omitempty"`
	Channels      int    `json:"channels,omitempty"`
	MaxBitRate    string `json:"max_bit_rate,omitempty"`
	SampleFmt     string `json:"sample_fmt,omitempty"`
	SampleRate    string `json:"sample_rate,omitempty"`
	SideDataList  []struct {
		Rotation int `json:"rotation"`
	} `json:"side_data_list"`
}
