package transcoder

import (
	"fmt"

	"github.com/stashapp/stash/pkg/ffmpeg"
)

type SceneDetectOptions struct {
	// Threshold is the scene-change sensitivity, 0-1. Higher values only
	// detect harder cuts. Defaults to 0.4 if not set.
	Threshold float64

	// DownscaleWidth, if > 0, scales the video down before analysis to
	// reduce CPU cost. Scene cuts are still detectable at low resolution.
	DownscaleWidth int

	// Verbosity is the logging verbosity. Defaults to LogLevelError if not set.
	Verbosity ffmpeg.LogLevel
}

func (o *SceneDetectOptions) setDefaults() {
	if o.Threshold <= 0 {
		o.Threshold = 0.4
	}
	if o.Verbosity == "" {
		o.Verbosity = ffmpeg.LogLevelError
	}
}

// SceneDetect builds ffmpeg args that decode input, run scene-change
// detection, and discard the actual frame output. Timestamps of detected
// cuts are written to stdout via the metadata print filter, one frame's
// metadata block per detected cut, each starting with a line containing
// "pts_time:<seconds>" - see ParseSceneCutTimestamps.
func SceneDetect(input string, options SceneDetectOptions) ffmpeg.Args {
	options.setDefaults()

	var args ffmpeg.Args
	args = args.LogLevel(options.Verbosity)
	args = args.Input(input)

	var vf ffmpeg.VideoFilter
	if options.DownscaleWidth > 0 {
		vf = vf.ScaleWidth(options.DownscaleWidth)
	}
	vf = vf.Append(fmt.Sprintf("select='gt(scene,%v)'", options.Threshold))
	vf = vf.Append("metadata=print:file=-")
	args = args.VideoFilter(vf)

	args = args.SkipAudio()
	args = args.Format("null")
	args = args.Output("-")

	return args
}
