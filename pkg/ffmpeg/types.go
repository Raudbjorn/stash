package ffmpeg

import (
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/utils"
)

// FFProbeTags preserves arbitrary format and stream metadata tags.
type FFProbeTags map[string]string

// Get returns a tag value using a case-insensitive key comparison.
func (t FFProbeTags) Get(key string) string {
	for candidate, value := range t {
		if strings.EqualFold(candidate, key) {
			return value
		}
	}
	return ""
}

// Time parses a tag value as a timestamp. Invalid or absent values return zero.
func (t FFProbeTags) Time(key string) time.Time {
	value := strings.TrimSpace(t.Get(key))
	if value == "" {
		return time.Time{}
	}
	ret, _ := utils.ParseDateStringAsTime(value)
	return ret
}

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
	Duration          string      `json:"duration"`
	DurationTs        int64       `json:"duration_ts"`
	HasBFrames        int         `json:"has_b_frames,omitempty"`
	Height            int         `json:"height,omitempty"`
	Index             int         `json:"index"`
	IsAvc             string      `json:"is_avc,omitempty"`
	Level             int         `json:"level,omitempty"`
	NalLengthSize     string      `json:"nal_length_size,omitempty"`
	NbFrames          string      `json:"nb_frames"`
	NbReadFrames      string      `json:"nb_read_frames"`
	PixFmt            string      `json:"pix_fmt,omitempty"`
	Profile           string      `json:"profile"`
	RFrameRate        string      `json:"r_frame_rate"`
	Refs              int         `json:"refs,omitempty"`
	SampleAspectRatio string      `json:"sample_aspect_ratio,omitempty"`
	StartPts          int64       `json:"start_pts"`
	StartTime         string      `json:"start_time"`
	Tags              FFProbeTags `json:"tags"`
	TimeBase          string      `json:"time_base"`
	Width             int         `json:"width,omitempty"`
	BitsPerSample     int         `json:"bits_per_sample,omitempty"`
	ChannelLayout     string      `json:"channel_layout,omitempty"`
	Channels          int         `json:"channels,omitempty"`
	MaxBitRate        string      `json:"max_bit_rate,omitempty"`
	SampleFmt         string      `json:"sample_fmt,omitempty"`
	SampleRate        string      `json:"sample_rate,omitempty"`
	SideDataList      []struct {
		Rotation int `json:"rotation"`
	} `json:"side_data_list"`
}
