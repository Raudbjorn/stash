package native

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The framing is where the off-by-one errors live, so it is tested directly;
// the model itself is injected, which is what makes that possible.

func TestFramePatchesOverlap(t *testing.T) {
	// Exactly four hops' worth of samples.
	samples := make([]float32, HopSamples()*4)
	patches := FramePatches(samples)

	if len(patches) == 0 {
		t.Fatal("no patches from a non-empty signal")
	}

	for i, patch := range patches {
		if len(patch.Samples) != WindowSamples() {
			t.Fatalf("patch %d has %d samples, want %d", i, len(patch.Samples), WindowSamples())
		}
	}

	// Consecutive patches are one hop apart, and the hop is shorter than the
	// window - that overlap is what stops a sound spanning a boundary being
	// split across two patches and scored weakly in both.
	if len(patches) > 1 {
		delta := patches[1].Start - patches[0].Start
		if math.Abs(delta-AudioHopSeconds) > 1e-6 {
			t.Errorf("patches are %v apart, want the hop %v", delta, AudioHopSeconds)
		}
	}
	if AudioHopSeconds >= AudioWindowSeconds {
		t.Fatal("the hop is not shorter than the window; patches do not overlap")
	}
}

// A trailing partial window is padded rather than dropped: the end of a scene
// is often where the distinctive audio is.
func TestFramePatchesPadsTheTail(t *testing.T) {
	// One full window plus half a hop: the first patch fills, the second
	// cannot. (A signal shorter than one window yields a single padded patch,
	// which is also correct but does not exercise the tail.)
	samples := make([]float32, WindowSamples()+HopSamples()/2)
	for i := range samples {
		samples[i] = 0.5
	}

	patches := FramePatches(samples)
	if len(patches) < 2 {
		t.Fatalf("got %d patches, want the tail to be kept", len(patches))
	}

	last := patches[len(patches)-1]
	if len(last.Samples) != WindowSamples() {
		t.Errorf("the padded patch has %d samples, want a full %d", len(last.Samples), WindowSamples())
	}

	// The padding is zeros, and there is real signal before it.
	if last.Samples[0] == 0 {
		t.Error("the tail patch contains no signal")
	}
	if last.Samples[len(last.Samples)-1] != 0 {
		t.Error("the tail patch was not zero-padded")
	}
}

func TestFramePatchesOnEmptyInput(t *testing.T) {
	if got := FramePatches(nil); got != nil {
		t.Errorf("FramePatches(nil) = %v, want nil", got)
	}
}

func TestScorePatchesThresholds(t *testing.T) {
	labels := []string{"speech", "music", "silence"}
	patches := []AudioPatch{{Start: 0}, {Start: 0.48}}

	scorer := func(patch AudioPatch) ([]float32, error) {
		if patch.Start == 0 {
			return []float32{0.9, 0.1, 0.2}, nil
		}
		return []float32{0.2, 0.8, 0.1}, nil
	}

	detections, err := ScorePatches(patches, scorer, labels, 0.5)
	if err != nil {
		t.Fatalf("ScorePatches: %v", err)
	}

	if len(detections) != 2 {
		t.Fatalf("detections = %+v, want two above threshold", detections)
	}
	if detections[0].Label != "speech" || detections[1].Label != "music" {
		t.Errorf("labels = %s, %s", detections[0].Label, detections[1].Label)
	}
	// The window's length, not the hop: a patch covers what it was scored on.
	if math.Abs(detections[0].End-AudioWindowSeconds) > 1e-6 {
		t.Errorf("end = %v, want the window length %v", detections[0].End, AudioWindowSeconds)
	}
}

// A score vector of the wrong width means the model and the label list disagree,
// which would otherwise silently mislabel everything.
func TestScorePatchesChecksLabelCount(t *testing.T) {
	scorer := func(AudioPatch) ([]float32, error) { return []float32{0.9}, nil }

	_, err := ScorePatches([]AudioPatch{{}}, scorer, []string{"a", "b"}, 0.5)
	if err == nil {
		t.Fatal("a score vector of the wrong width was accepted")
	}
}

func TestScorePatchesPropagatesErrors(t *testing.T) {
	wanted := errors.New("model failed")
	scorer := func(AudioPatch) ([]float32, error) { return nil, wanted }

	if _, err := ScorePatches([]AudioPatch{{}}, scorer, []string{"a"}, 0.5); !errors.Is(err, wanted) {
		t.Errorf("err = %v, want the scorer's own error", err)
	}
}

// Audio extraction is checked against a real generated file: the sample rate,
// channel count and scaling are all things ffmpeg gets right or wrong for us.
func TestReadPatchesFromRealAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}

	path := filepath.Join(t.TempDir(), "tone.mp4")
	cmd := exec.Command(ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=5",
		"-f", "lavfi", "-i", "color=c=black:s=64x64:d=5:r=10",
		"-map", "0:a", "-map", "1:v",
		"-pix_fmt", "yuv420p", "-shortest", path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build an audio fixture: %v: %s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	patches, err := ReadPatches(ctx, ffmpeg, path, 0)
	if err != nil {
		t.Fatalf("ReadPatches: %v", err)
	}

	// Five seconds at a 0.48s hop.
	hop := float64(AudioHopSeconds)
	expected := int(5.0/hop) + 1
	if len(patches) < expected-2 || len(patches) > expected+2 {
		t.Errorf("got %d patches from 5 seconds, expected about %d", len(patches), expected)
	}

	// A 440 Hz sine is loud and centred, so the samples must be non-trivial and
	// within the scaled range. Values outside [-1,1] would mean the int16
	// scaling was skipped, which produces confident nonsense from the model.
	var peak float32
	for _, sample := range patches[0].Samples {
		if sample > peak {
			peak = sample
		}
		if sample > 1 || sample < -1 {
			t.Fatalf("sample %v is outside [-1,1]; the int16 scaling is wrong", sample)
		}
	}
	if peak < 0.1 {
		t.Errorf("peak amplitude %v; the audio appears to be silence", peak)
	}
}

// A file with no audio must say so rather than producing an empty analysis a
// caller would read as "this scene is silent".
func TestReadPatchesReportsMissingAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}

	video := makeVideo(t, ffmpeg, 3, 64, 64)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	_, err = ReadPatches(ctx, ffmpeg, video, 0)
	if !errors.Is(err, ErrNoAudio) {
		t.Errorf("ReadPatches on a silent file = %v, want ErrNoAudio", err)
	}
}

func TestReadPatchesHonoursTheLimit(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}

	path := filepath.Join(t.TempDir(), "long.mp4")
	cmd := exec.Command(ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=20",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=64x64:d=%d:r=5", 20),
		"-map", "0:a", "-map", "1:v", "-pix_fmt", "yuv420p", "-shortest", path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build a fixture: %v: %s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	patches, err := ReadPatches(ctx, ffmpeg, path, 4)
	if err != nil {
		t.Fatalf("ReadPatches: %v", err)
	}

	last := patches[len(patches)-1].Start
	if last > 5 {
		t.Errorf("the last patch starts at %v despite a 4-second limit", last)
	}
}
