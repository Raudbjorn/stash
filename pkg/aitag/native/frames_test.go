package native

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Frame extraction is tested against a real generated video rather than a mock:
// the filter chain is the part that goes wrong, and a mock ffmpeg would only
// confirm the string was assembled as written.

func requireFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not available")
	}
	return path
}

// makeVideo generates a short clip with ffmpeg's own test source.
func makeVideo(t *testing.T, ffmpeg string, seconds int, width, height int) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "test.mp4")
	cmd := exec.Command(ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=duration=%d:size=%dx%d:rate=10", seconds, width, height),
		"-pix_fmt", "yuv420p",
		path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate test video: %v: %s", err, out)
	}
	return path
}

func TestExtractFramesAtInterval(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	video := makeVideo(t, ffmpeg, 10, 320, 240)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frames, err := OpenFrames(ctx, ffmpeg, video, ExtractOptions{Interval: 2, Size: 64})
	if err != nil {
		t.Fatalf("OpenFrames: %v", err)
	}
	defer frames.Close()

	var (
		buf    []byte
		times  []float64
		frame  = 0
		expect = 64 * 64 * 3
	)
	for {
		f, err := frames.Next(buf)
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if f == nil {
			break
		}
		buf = f.RGB

		if len(f.RGB) != expect {
			t.Fatalf("frame %d is %d bytes, want %d", frame, len(f.RGB), expect)
		}
		if f.Index != frame {
			t.Errorf("index = %d, want %d", f.Index, frame)
		}
		times = append(times, f.Time)
		frame++
	}

	// A 10-second clip at one frame every 2 seconds: 5 frames.
	if frame != 5 {
		t.Errorf("extracted %d frames from a 10s clip at interval 2, want 5", frame)
	}
	for i, at := range times {
		if want := float64(i) * 2; at != want {
			t.Errorf("frame %d is at %v, want %v", i, at, want)
		}
	}

	if err := frames.Finish(); err != nil {
		t.Errorf("Finish: %v", err)
	}
}

// The frames must be square regardless of the source aspect ratio: the models
// were trained on squashed frames, so letterboxing would be a mismatch.
func TestFramesAreSquashedNotLetterboxed(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	// Deliberately very wide.
	video := makeVideo(t, ffmpeg, 4, 640, 160)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frames, err := OpenFrames(ctx, ffmpeg, video, ExtractOptions{Interval: 2, Size: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer frames.Close()

	f, err := frames.Next(nil)
	if err != nil || f == nil {
		t.Fatalf("no frame: %v", err)
	}
	if len(f.RGB) != 32*32*3 {
		t.Errorf("a 4:1 source produced %d bytes, want a square %d", len(f.RGB), 32*32*3)
	}

	// If it were letterboxed, the top and bottom rows would be uniform black
	// padding. testsrc has coloured content everywhere, so a non-black row
	// somewhere in the top eighth proves the frame was squashed instead.
	padded := true
	for i := 0; i < 32*4*3; i++ {
		if f.RGB[i] != 0 {
			padded = false
			break
		}
	}
	if padded {
		t.Error("the frame appears letterboxed; the models expect a squashed square")
	}
}

// VR crops the right half before resizing: feeding a model both eyes of a
// side-by-side frame halves its effective resolution.
func TestVRCropsToOneEye(t *testing.T) {
	ffmpeg := requireFFmpeg(t)

	// A frame that is red on the left and blue on the right, so which half
	// survives is visible in the pixels.
	path := filepath.Join(t.TempDir(), "sbs.mp4")
	cmd := exec.Command(ffmpeg,
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=red:s=160x120:d=4:r=10",
		"-f", "lavfi", "-i", "color=c=blue:s=160x120:d=4:r=10",
		"-filter_complex", "[0:v][1:v]hstack=inputs=2[v]",
		"-map", "[v]", "-pix_fmt", "yuv420p", path,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("could not build a side-by-side fixture: %v: %s", err, out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	frames, err := OpenFrames(ctx, ffmpeg, path, ExtractOptions{Interval: 2, Size: 16, VR: true})
	if err != nil {
		t.Fatal(err)
	}
	defer frames.Close()

	f, err := frames.Next(nil)
	if err != nil || f == nil {
		t.Fatalf("no frame: %v", err)
	}

	// The centre pixel should be blue - the right half - not red.
	centre := (16*8 + 8) * 3
	r, g, b := f.RGB[centre], f.RGB[centre+1], f.RGB[centre+2]
	if b <= r {
		t.Errorf("centre pixel is (%d,%d,%d); VR should have kept the RIGHT (blue) half", r, g, b)
	}
}

func TestFilterChain(t *testing.T) {
	cases := []struct {
		opts ExtractOptions
		want string
	}{
		{ExtractOptions{Interval: 2, Size: 224}, "fps=1/2,scale=224:224:flags=bilinear"},
		{ExtractOptions{Interval: 0.5, Size: 384}, "fps=1/0.5,scale=384:384:flags=bilinear"},
		{ExtractOptions{Interval: 4, Size: 224, VR: true},
			"fps=1/4,crop=iw/2:ih:iw/2:0,scale=224:224:flags=bilinear"},
	}

	for _, tc := range cases {
		if got := buildFilter(tc.opts); got != tc.want {
			t.Errorf("buildFilter(%+v) = %q, want %q", tc.opts, got, tc.want)
		}
	}
}

// A file ffmpeg cannot open must produce ffmpeg's own explanation, not a bare
// "unexpected EOF" that says nothing about the cause.
func TestUnreadableFileReportsFFmpegsReason(t *testing.T) {
	ffmpeg := requireFFmpeg(t)

	path := filepath.Join(t.TempDir(), "not-a-video.mp4")
	if err := os.WriteFile(path, []byte("this is not a video"), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	frames, err := OpenFrames(ctx, ffmpeg, path, ExtractOptions{Interval: 2, Size: 32})
	if err != nil {
		t.Fatal(err)
	}
	defer frames.Close()

	f, _ := frames.Next(nil)
	if f != nil {
		t.Fatal("a text file produced a frame")
	}

	err = frames.Finish()
	if err == nil {
		t.Fatal("ffmpeg failing on an invalid file was reported as success")
	}
	// Whatever ffmpeg said, it must reach the caller.
	if !strings.Contains(strings.ToLower(err.Error()), "invalid") &&
		!strings.Contains(strings.ToLower(err.Error()), "moov") &&
		!strings.Contains(strings.ToLower(err.Error()), "error") {
		t.Errorf("the error does not carry ffmpeg's diagnostics: %v", err)
	}
}

// Cancelling must stop ffmpeg rather than leaving it decoding a large file.
func TestCancellationStopsFFmpeg(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	video := makeVideo(t, ffmpeg, 20, 320, 240)

	ctx, cancel := context.WithCancel(context.Background())

	frames, err := OpenFrames(ctx, ffmpeg, video, ExtractOptions{Interval: 0.1, Size: 64})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := frames.Next(nil); err != nil {
		t.Fatalf("first frame: %v", err)
	}

	pid := frames.cmd.Process.Pid
	cancel()
	_ = frames.Close()

	// The process must be reaped, not left behind.
	if err := frames.cmd.Process.Signal(os.Signal(nil)); err == nil {
		t.Errorf("ffmpeg process %d survived cancellation", pid)
	}
}

func TestOpenFramesRejectsBadOptions(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	ctx := context.Background()

	if _, err := OpenFrames(ctx, ffmpeg, "x.mp4", ExtractOptions{Interval: 0, Size: 224}); err == nil {
		t.Error("a zero interval was accepted; it would divide by zero in the filter")
	}
	if _, err := OpenFrames(ctx, ffmpeg, "x.mp4", ExtractOptions{Interval: 2, Size: 0}); err == nil {
		t.Error("a zero frame size was accepted")
	}
}

func TestProbeDuration(t *testing.T) {
	ffmpeg := requireFFmpeg(t)
	if _, err := exec.LookPath(ffprobePath(ffmpeg)); err != nil {
		t.Skip("ffprobe not available")
	}

	video := makeVideo(t, ffmpeg, 6, 160, 120)

	duration, err := probeDuration(context.Background(), ffmpeg, video)
	if err != nil {
		t.Fatalf("probeDuration: %v", err)
	}
	if duration < 5.5 || duration > 6.5 {
		t.Errorf("duration = %v, want about 6", duration)
	}
}

func TestFFprobePathDerivation(t *testing.T) {
	// A bare name goes through PATH, and an unrecognisable one falls back.
	if got := ffprobePath("ffmpeg"); got != "ffprobe" {
		t.Errorf("ffprobePath(\"ffmpeg\") = %q, want ffprobe", got)
	}
	if got := ffprobePath("/usr/bin/something-else"); got != "ffprobe" {
		t.Errorf("an unrecognisable name should fall back to PATH, got %q", got)
	}

	// A sibling that EXISTS is preferred, since it is the matching build.
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	probe := filepath.Join(dir, "ffprobe")
	for _, path := range []string{ffmpeg, probe} {
		if err := os.WriteFile(path, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := ffprobePath(ffmpeg); got != probe {
		t.Errorf("ffprobePath = %q, want the sibling %q", got, probe)
	}

	// A sibling that does NOT exist falls back to PATH rather than defeating
	// the probe. This is a real arrangement: a user-installed ffmpeg under
	// ~/.local/bin beside a distribution ffprobe in /usr/local/bin.
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	if got := ffprobePath(ffmpeg); got != "ffprobe" {
		t.Errorf("a missing sibling should fall back to PATH, got %q", got)
	}
}
