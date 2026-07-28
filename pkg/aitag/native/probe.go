package native

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Video duration, read with ffprobe.
//
// Needed for two things: a progress estimate, and the percentage-based marker
// rules ("5%" of the video). Neither is worth failing an analysis over, so a
// probe failure is recoverable - the duration is recovered from the frames
// afterwards.

// probeTimeout bounds a duration probe. Reading a header is fast; a probe that
// takes longer than this is stuck on a broken file.
const probeTimeout = 30 * time.Second

// ProbeDuration returns a video's source duration using ffprobe.
func ProbeDuration(ctx context.Context, ffmpegPath, videoPath string) (float64, error) {
	return probeDuration(ctx, ffmpegPath, videoPath)
}

// probeDuration returns a file's duration in seconds.
func probeDuration(ctx context.Context, ffmpegPath, videoPath string) (float64, error) {
	ffprobe := ffprobePath(ffmpegPath)

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ffprobe,
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		videoPath,
	)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("ffprobe %s: %w: %s", videoPath, err, strings.TrimSpace(stderr.String()))
	}

	text := strings.TrimSpace(stdout.String())
	if text == "" || text == "N/A" {
		// A container with no duration in its header is legal, not broken.
		return 0, fmt.Errorf("no duration reported for %s", videoPath)
	}

	duration, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("unparseable duration %q for %s", text, videoPath)
	}
	return duration, nil
}

// ffprobePath derives the ffprobe path from ffmpeg's, falling back to PATH.
//
// They usually live side by side - in a distribution's bin directory, or in
// Stash's own downloaded copy - so deriving is more reliable than a second
// setting the user has to keep in step. But "usually" is not "always": a
// user-installed ffmpeg under ~/.local/bin alongside a distribution ffprobe in
// /usr/local/bin is a real arrangement, and a derived path that does not exist
// must not defeat the probe.
func ffprobePath(ffmpegPath string) string {
	derived := deriveSibling(ffmpegPath)
	if derived == "" {
		return "ffprobe"
	}

	// A bare name is resolved through PATH by exec, so there is nothing to
	// check; a rooted one either exists or does not.
	if !strings.ContainsRune(derived, os.PathSeparator) && !strings.ContainsRune(derived, '/') {
		return derived
	}
	if _, err := os.Stat(derived); err == nil {
		return derived
	}
	return "ffprobe"
}

// deriveSibling renames ffmpeg to ffprobe in place, or returns "" if the name
// is not recognisable.
func deriveSibling(ffmpegPath string) string {
	dir := filepath.Dir(ffmpegPath)
	base := filepath.Base(ffmpegPath)

	replaced := strings.Replace(base, "ffmpeg", "ffprobe", 1)
	if replaced == base {
		return ""
	}
	if dir == "." {
		return replaced
	}
	return filepath.Join(dir, replaced)
}
