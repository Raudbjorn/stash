package generate

import (
	"context"

	"github.com/stashapp/stash/pkg/fsutil"
	"github.com/stashapp/stash/pkg/logger"
)

// defaultClipDuration is used when a clip has no usable end time
// (nil end, or end <= start), so the generated video still has a bound.
const defaultClipDuration = 30

// clipDuration resolves how long a clip video should be from its
// [seconds, endSeconds] range. A positive interval is used directly;
// otherwise it falls back to defaultClipDuration.
func clipDuration(seconds float64, endSeconds *float64) float64 {
	if endSeconds != nil {
		if interval := *endSeconds - seconds; interval > 0 {
			return interval
		}
	}
	return defaultClipDuration
}

// ClipVideo extracts the bounded [seconds, endSeconds] range from the parent
// scene's video file (input) and writes an mp4 to the clip's generated path.
// It reuses the frame-accurate marker preview transcode.
func (g Generator) ClipVideo(ctx context.Context, input string, sceneHash string, clipID int, seconds float64, endSeconds *float64, includeAudio bool) error {
	lockCtx := g.LockManager.ReadLock(ctx, input)
	defer lockCtx.Cancel()

	output := g.ClipsPaths.GetClipVideoPath(sceneHash, clipID)
	if !g.Overwrite {
		if exists, _ := fsutil.FileExists(output); exists {
			return nil
		}
	}

	duration := clipDuration(seconds, endSeconds)

	if err := g.generateFile(lockCtx, g.ClipsPaths, mp4Pattern, output, g.markerPreviewVideo(input, sceneMarkerOptions{
		Seconds:  seconds,
		Duration: duration,
		Audio:    includeAudio,
	})); err != nil {
		return err
	}

	logger.Debug("created clip video: ", output)

	return nil
}
