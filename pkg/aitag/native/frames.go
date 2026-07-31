// Package native is the in-process ONNX inference pipeline.
//
// It replaces the proprietary engine with open models, and produces results in
// exactly the shape the HTTP provider does so the two are interchangeable and a
// library can be migrated scene by scene.
package native

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Frame extraction.
//
// ONE ffmpeg process streaming rawvideo, not one seek per frame. A 30-minute
// scene at a frame every two seconds is 900 frames; seeking to each would mean
// 900 process spawns and 900 decoder initialisations, which costs far more than
// the inference it feeds. Decoding once and sampling as it goes is the whole
// difference between "a background job" and "unusable".

// ErrNoFrames reports a file that produced nothing.
var ErrNoFrames = errors.New("no frames could be extracted")

// FrameSource yields decoded, resized RGB frames.
type FrameSource struct {
	// Width and Height are the frame dimensions, already resized.
	Width  int
	Height int
	// Interval is the sampling period in seconds.
	Interval float64

	cmd    *exec.Cmd
	stdout io.ReadCloser
	reader *bufio.Reader
	stderr *strings.Builder

	index int

	closeOnce sync.Once
	closeErr  error
}

// ExtractOptions configure frame extraction.
type ExtractOptions struct {
	// Interval is the sampling period in seconds. Must be positive.
	Interval float64
	// Size is the square side the frames are resized to.
	Size int
	// VR crops the right half of a side-by-side 180 frame before resizing.
	VR bool
	// Resample is the ffmpeg scaler flag. Empty uses bilinear.
	//
	// It matters which: SigLIP's published processor specifies bicubic, and
	// resizing differently from how a model was trained is a small, invisible
	// accuracy loss rather than an error.
	Resample string
	// StartAt skips into the file, in seconds.
	StartAt float64
}

// FrameData is one sampled frame.
type FrameData struct {
	// Index is the frame's position in the sequence.
	Index int
	// Time is its timestamp in seconds.
	Time float64
	// RGB is Width*Height*3 bytes, row-major.
	RGB []byte
}

// OpenFrames starts ffmpeg and returns a source of resized frames.
//
// The caller must Close the source, which kills ffmpeg: without that a
// cancelled analysis leaves a decoder running over a large file.
func OpenFrames(ctx context.Context, ffmpegPath, videoPath string, opts ExtractOptions) (*FrameSource, error) {
	if opts.Interval <= 0 {
		return nil, fmt.Errorf("frame interval must be positive, got %v", opts.Interval)
	}
	if opts.Size <= 0 {
		return nil, fmt.Errorf("frame size must be positive, got %d", opts.Size)
	}

	args := []string{
		// Never read stdin: ffmpeg otherwise consumes the parent's, which in a
		// server means whatever else was on it.
		"-nostdin",
		"-hide_banner",
		"-loglevel", "error",
	}
	if opts.StartAt > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", opts.StartAt))
	}
	args = append(args,
		"-i", videoPath,
		// No audio: it is a separate concern and decoding it here is waste.
		"-an",
		"-sn",
		"-vf", buildFilter(opts),
		"-f", "rawvideo",
		"-pix_fmt", "rgb24",
		"-",
	)

	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	// ffmpeg's diagnostics are the only explanation of a failure, and they are
	// lost when the process exits, so they are captured rather than discarded.
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	return &FrameSource{
		Width:    opts.Size,
		Height:   opts.Size,
		Interval: opts.Interval,
		cmd:      cmd,
		stdout:   stdout,
		// A large buffer: a 224x224 RGB frame is 150 KB, and reading it in
		// pipe-sized pieces would mean dozens of syscalls per frame.
		reader: bufio.NewReaderSize(stdout, 1<<20),
		stderr: &stderr,
	}, nil
}

// buildFilter assembles the ffmpeg filter chain.
//
// Three steps, in this order:
//
//  1. fps: sample at the requested interval. Doing this in the filter rather
//     than by seeking is what keeps it to one decode pass.
//  2. crop, for VR only: a side-by-side 180 frame carries two eyes, and feeding
//     both to a model that expects one image halves its effective resolution.
//     The RIGHT half is taken, matching the reference implementation.
//  3. scale: to a square, WITHOUT preserving aspect ratio. The models were
//     trained on squashed frames, so letterboxing here would be a mismatch -
//     this looks like a bug and is not.
func buildFilter(opts ExtractOptions) string {
	parts := []string{fmt.Sprintf("fps=1/%g", opts.Interval)}

	if opts.VR {
		// Applied unconditionally when VR is set rather than by testing the
		// aspect ratio, because the caller knows what the file is and ffmpeg
		// cannot branch on it mid-chain.
		parts = append(parts, "crop=iw/2:ih:iw/2:0")
	}

	flags := opts.Resample
	if flags == "" {
		flags = "bilinear"
	}
	parts = append(parts, fmt.Sprintf("scale=%d:%d:flags=%s", opts.Size, opts.Size, flags))
	return strings.Join(parts, ",")
}

// Next reads the next frame, returning nil at the end of the stream.
//
// The returned buffer is reused between calls, so a caller that keeps a frame
// must copy it. That is deliberate: allocating 150 KB per frame for 900 frames
// would produce 135 MB of garbage per scene for no reason.
func (s *FrameSource) Next(buf []byte) (*FrameData, error) {
	size := s.Width * s.Height * 3
	if cap(buf) < size {
		buf = make([]byte, size)
	}
	buf = buf[:size]

	if _, err := io.ReadFull(s.reader, buf); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		// A partial frame means ffmpeg died mid-write, which its stderr will
		// explain far better than "unexpected EOF" does.
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, s.exitError(fmt.Errorf("ffmpeg produced a partial frame"))
		}
		return nil, err
	}

	frame := &FrameData{
		Index: s.index,
		Time:  float64(s.index) * s.Interval,
		RGB:   buf,
	}
	s.index++
	return frame, nil
}

// Count is how many frames have been read.
func (s *FrameSource) Count() int { return s.index }

// Close stops ffmpeg and releases the pipe.
func (s *FrameSource) Close() error {
	s.closeOnce.Do(func() {
		// Closing the read end first makes ffmpeg see EPIPE and exit, rather
		// than blocking forever writing to a pipe nobody is draining.
		_ = s.stdout.Close()

		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		err := s.cmd.Wait()

		// A killed process is the normal path here, not a failure.
		if err != nil && !isKilled(err) {
			s.closeErr = s.exitError(err)
		}
	})
	return s.closeErr
}

// Finish waits for ffmpeg to exit normally, reporting a genuine failure.
//
// Distinct from Close: a caller that read to EOF wants to know whether ffmpeg
// finished cleanly, while one abandoning the stream does not.
func (s *FrameSource) Finish() error {
	var err error
	s.closeOnce.Do(func() {
		_ = s.stdout.Close()
		if waitErr := s.cmd.Wait(); waitErr != nil && !isKilled(waitErr) {
			s.closeErr = s.exitError(waitErr)
		}
		err = s.closeErr
	})
	if err == nil {
		err = s.closeErr
	}
	return err
}

// exitError attaches ffmpeg's diagnostics to a failure.
func (s *FrameSource) exitError(err error) error {
	message := strings.TrimSpace(s.stderr.String())
	if message == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, truncate(message, 500))
}

func isKilled(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	// A signalled process reports no exit code.
	return exitErr.ExitCode() == -1
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
