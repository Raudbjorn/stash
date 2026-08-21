package librarytranscode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
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
}

func nvencSpec(scaleFilter string, cq int, audioBitrate string) EncodeSpec {
	return EncodeSpec{
		ScaleFilter:  scaleFilter,
		VideoCodec:   ffmpeg.VideoCodecN264,
		VideoArgs:    ffmpeg.Args{"-preset", "p4", "-cq", fmt.Sprint(cq)},
		AudioBitrate: audioBitrate,
		UseCUDA:      true,
	}
}

// NVENCSpec returns the NVENC encode spec for a target height.
func NVENCSpec(targetHeight int, cq int, audioBitrate string, fps int) EncodeSpec {
	scale := fmt.Sprintf("scale_cuda=-2:%d", targetHeight)
	if fps > 0 {
		scale = fmt.Sprintf("scale_cuda=-2:%d,fps=fps=%d", targetHeight, fps)
	}
	return nvencSpec(scale, cq, audioBitrate)
}

// SoftwareX265Spec is MAX_360 fallback (2).
func SoftwareX265Spec() EncodeSpec {
	return EncodeSpec{
		ScaleFilter:  "scale=-2:360,fps=fps=24",
		VideoCodec:   ffmpeg.VideoCodecLibX265,
		VideoArgs:    ffmpeg.Args{"-preset", "slow", "-crf", "35", "-g", "48", "-pix_fmt", "yuv420p"},
		AudioBitrate: "48k",
	}
}

// SoftwareX264Spec is MAX_360 fallback (3).
func SoftwareX264Spec() EncodeSpec {
	return EncodeSpec{
		ScaleFilter:  "scale=-2:360,fps=fps=24",
		VideoCodec:   ffmpeg.VideoCodecLibX264,
		VideoArgs:    ffmpeg.Args{"-preset", "medium", "-crf", "32", "-g", "48", "-pix_fmt", "yuv420p"},
		AudioBitrate: "48k",
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

	args := transcoder.Transcode(inputPath, transcoder.TranscodeOptions{
		OutputPath:      outputPath,
		Format:          ffmpeg.FormatMP4,
		VideoCodec:      spec.VideoCodec,
		VideoArgs:       spec.VideoArgs,
		AudioCodec:      ffmpeg.AudioCodecAAC,
		AudioArgs:       ffmpeg.Args{"-b:a", spec.AudioBitrate},
		ExtraInputArgs:  extraInput,
		ExtraOutputArgs: []string{"-vf", spec.ScaleFilter, "-movflags", "+faststart"},
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
