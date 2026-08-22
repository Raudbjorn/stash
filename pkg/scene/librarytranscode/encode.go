package librarytranscode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
	"github.com/stashapp/stash/pkg/logger"
)

// EncodeSpec describes one encode attempt.
type EncodeSpec struct {
	ScaleFilter  string
	VideoCodec   ffmpeg.VideoCodec
	VideoArgs    ffmpeg.Args
	AudioBitrate string
	UseCUDA      bool
	// FrameRate, when > 0, is set as the output -r (keeps fps out of CUDA graphs).
	FrameRate int
}

func nvencSpec(scaleFilter string, cq int, audioBitrate string, fps int) EncodeSpec {
	return EncodeSpec{
		ScaleFilter:  scaleFilter,
		VideoCodec:   ffmpeg.VideoCodecN264,
		VideoArgs:    ffmpeg.Args{"-preset", "p4", "-cq", fmt.Sprint(cq)},
		AudioBitrate: audioBitrate,
		UseCUDA:      true,
		FrameRate:    fps,
	}
}

// MinSideScaleFilter scales the short side to target. Even long side via -2.
func MinSideScaleFilter(target, width, height int, cuda bool) string {
	name := "scale"
	if cuda {
		name = "scale_cuda"
	}
	if height > width {
		return fmt.Sprintf("%s=%d:-2", name, target)
	}
	return fmt.Sprintf("%s=-2:%d", name, target)
}

// NVENCSpec returns the NVENC encode spec for a target min-side.
func NVENCSpec(target int, cq int, audioBitrate string, fps int, width, height int) EncodeSpec {
	return nvencSpec(MinSideScaleFilter(target, width, height, true), cq, audioBitrate, fps)
}

// SoftwareX265Spec is MAX_360 fallback (2).
func SoftwareX265Spec(width, height int) EncodeSpec {
	return EncodeSpec{
		ScaleFilter:  MinSideScaleFilter(360, width, height, false),
		VideoCodec:   ffmpeg.VideoCodecLibX265,
		VideoArgs:    ffmpeg.Args{"-preset", "slow", "-crf", "35", "-g", "48", "-pix_fmt", "yuv420p"},
		AudioBitrate: "48k",
		FrameRate:    24,
	}
}

// SoftwareX264Spec is MAX_360 fallback (3).
func SoftwareX264Spec(width, height int) EncodeSpec {
	return EncodeSpec{
		ScaleFilter:  MinSideScaleFilter(360, width, height, false),
		VideoCodec:   ffmpeg.VideoCodecLibX264,
		VideoArgs:    ffmpeg.Args{"-preset", "medium", "-crf", "32", "-g", "48", "-pix_fmt", "yuv420p"},
		AudioBitrate: "48k",
		FrameRate:    24,
	}
}

// Encode runs ffmpeg to transcode input to output using spec.
func Encode(ctx context.Context, encoder *ffmpeg.FFMpeg, inputPath, outputPath string, spec EncodeSpec) error {
	if encoder == nil {
		return errors.New("ffmpeg not available")
	}

	var extraInput []string
	if spec.UseCUDA {
		extraInput = []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}
	}

	extraOut := []string{"-vf", spec.ScaleFilter, "-movflags", "+faststart"}
	if spec.FrameRate > 0 {
		extraOut = append(extraOut, "-r", strconv.Itoa(spec.FrameRate))
	}

	args := transcoder.Transcode(inputPath, transcoder.TranscodeOptions{
		OutputPath:      outputPath,
		Format:          ffmpeg.FormatMP4,
		VideoCodec:      spec.VideoCodec,
		VideoArgs:       spec.VideoArgs,
		AudioCodec:      ffmpeg.AudioCodecAAC,
		AudioArgs:       ffmpeg.Args{"-b:a", spec.AudioBitrate},
		ExtraInputArgs:  extraInput,
		ExtraOutputArgs: extraOut,
	})

	logger.Tracef("library transcode: ffmpeg %s", strings.Join(args.Args(), " "))

	cmd := encoder.Command(ctx, args.Args())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.Bytes()
			err = exitErr
		}
		return fmt.Errorf("error running ffmpeg command <%s>: %w", strings.Join(args.Args(), " "), err)
	}

	return nil
}
