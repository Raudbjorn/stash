package generate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/stashapp/stash/pkg/ffmpeg/transcoder"
)

// DetectSceneCuts runs ffmpeg scene-change detection on input and returns
// the sorted timestamps (in seconds) of detected hard cuts. Returns an
// empty, non-nil slice if no cuts are detected - this is a valid result,
// not an error (e.g. for a single continuous take).
func (g Generator) DetectSceneCuts(ctx context.Context, input string, options transcoder.SceneDetectOptions) ([]float64, error) {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	args := transcoder.SceneDetect(input, options)

	cmd := g.Encoder.Command(lockCtx, args)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("error starting command: %w", err)
	}

	lockCtx.AttachCommand(cmd)

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitErr.Stderr = stderr.Bytes()
			err = exitErr
		}
		return nil, fmt.Errorf("error running ffmpeg command <%s>: %w", strings.Join(args, " "), err)
	}

	return ParseSceneCutTimestamps(stdout.Bytes())
}

// ParseSceneCutTimestamps parses the output of ffmpeg's metadata=print filter
// (see transcoder.SceneDetect) into a sorted list of timestamps in seconds.
// Each detected cut emits a metadata block whose first line contains
// "pts_time:<seconds>"; this extracts and sorts those values. Returns an
// empty, non-nil slice if no cuts are present in the output.
func ParseSceneCutTimestamps(output []byte) ([]float64, error) {
	ret := []float64{}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		idx := strings.Index(line, "pts_time:")
		if idx == -1 {
			continue
		}

		rest := line[idx+len("pts_time:"):]
		// pts_time is followed by whitespace or end of line
		end := strings.IndexAny(rest, " \t\r")
		if end != -1 {
			rest = rest[:end]
		}

		seconds, err := strconv.ParseFloat(strings.TrimSpace(rest), 64)
		if err != nil {
			return nil, fmt.Errorf("parsing pts_time value %q: %w", rest, err)
		}

		ret = append(ret, seconds)
	}

	sort.Float64s(ret)

	return ret, nil
}
