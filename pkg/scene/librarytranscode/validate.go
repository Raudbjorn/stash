package librarytranscode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"

	"github.com/stashapp/stash/pkg/ffmpeg"
	"github.com/stashapp/stash/pkg/scene/transcodebenefit"
)

// outputOK checks numeric validation rules without invoking ffmpeg.
func outputOK(minSide, target int, srcDur, outDur float64, size int64) error {
	if size <= 0 {
		return errors.New("output size is 0")
	}
	if minSide < target-16 || minSide > target+16 {
		return fmt.Errorf("output min-side %d not in [%d, %d]", minSide, target-16, target+16)
	}
	tol := math.Max(0.5, 0.02*srcDur)
	if math.Abs(outDur-srcDur) > tol {
		return fmt.Errorf("output duration %.3f differs from source %.3f (tol %.3f)", outDur, srcDur, tol)
	}
	return nil
}

// Validate checks the encoded file. On failure it deletes path.
func Validate(ctx context.Context, encoder *ffmpeg.FFMpeg, probe *ffmpeg.FFProbe, path string, target int, srcDuration float64) error {
	fail := func(err error) error {
		_ = os.Remove(path)
		return err
	}

	st, err := os.Stat(path)
	if err != nil {
		return fail(fmt.Errorf("stat output: %w", err))
	}

	if probe == nil {
		return fail(errors.New("ffprobe not available"))
	}

	vf, err := probe.NewVideoFileContext(ctx, path)
	if err != nil {
		return fail(fmt.Errorf("ffprobe output: %w", err))
	}
	if vf.VideoStream == nil {
		return fail(errors.New("output has no video stream"))
	}

	minSide := transcodebenefit.MinSide(vf.Width, vf.Height)
	if err := outputOK(minSide, target, srcDuration, vf.FileDuration, st.Size()); err != nil {
		return fail(err)
	}

	if encoder == nil {
		return fail(errors.New("ffmpeg not available"))
	}

	args := ffmpeg.Args{}
	args = args.LogLevel(ffmpeg.LogLevelError)
	args = args.Input(path)
	args = args.Duration(1)
	args = append(args, "-f", "null")
	args = args.NullOutput()

	cmd := encoder.Command(ctx, args.Args())
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fail(fmt.Errorf("decode gate failed: %w", err))
		}
		return fail(fmt.Errorf("decode gate: %w", err))
	}
	if strings.Contains(stderr.String(), "Error") {
		return fail(fmt.Errorf("decode gate stderr: %s", stderr.String()))
	}

	return nil
}
