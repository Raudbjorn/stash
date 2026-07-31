package native

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// Audio tagging.
//
// The audio track carries information the frames do not - speech, moaning,
// music, silence - and it is cheap: decoding audio is a fraction of the cost of
// decoding video, and YAMNet is a small model.
//
// Extraction mirrors the video path: ONE ffmpeg process streaming raw PCM, not
// a temporary wav file. A feature-length track at 16 kHz mono is 100 MB of wav
// that would be written and read back for no reason.

// YAMNet's input contract. These are the model's, not choices: it was trained
// on 16 kHz mono, and its framing determines what its scores mean.
const (
	// AudioSampleRate is the rate YAMNet expects.
	AudioSampleRate = 16000
	// AudioWindowSeconds is the length of one scored patch.
	AudioWindowSeconds = 0.96
	// AudioHopSeconds is the step between patches. Shorter than the window, so
	// consecutive patches overlap and a sound spanning a boundary is not split.
	AudioHopSeconds = 0.48
)

// ErrNoAudio reports a file with no audio track.
var ErrNoAudio = errors.New("the file has no audio track")

// AudioSource yields mono float32 samples decoded from a file.
type AudioSource struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	reader *bufio.Reader
	stderr *strings.Builder

	closeOnce sync.Once
	closeErr  error
}

// OpenAudio starts ffmpeg decoding a file's audio to 16 kHz mono PCM.
func OpenAudio(ctx context.Context, ffmpegPath, path string) (*AudioSource, error) {
	cmd := exec.CommandContext(ctx, ffmpegPath,
		"-nostdin", "-hide_banner", "-loglevel", "error",
		"-i", path,
		// No video, no subtitles: decoding them would dominate the cost of the
		// audio this actually wants.
		"-vn", "-sn", "-dn",
		"-ac", "1",
		"-ar", fmt.Sprint(AudioSampleRate),
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}

	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	return &AudioSource{
		cmd:    cmd,
		stdout: stdout,
		reader: bufio.NewReaderSize(stdout, 1<<20),
		stderr: &stderr,
	}, nil
}

// Read fills buf with samples, returning how many were read.
//
// Samples are scaled to [-1, 1), which is what the model expects; feeding it
// raw int16 would produce scores that look like confident nonsense.
func (a *AudioSource) Read(buf []float32) (int, error) {
	raw := make([]byte, len(buf)*2)

	n, err := io.ReadFull(a.reader, raw)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return 0, err
	}

	samples := n / 2
	for i := 0; i < samples; i++ {
		value := int16(binary.LittleEndian.Uint16(raw[i*2:]))
		buf[i] = float32(value) / 32768
	}
	return samples, nil
}

// Close stops ffmpeg.
func (a *AudioSource) Close() error {
	a.closeOnce.Do(func() {
		_ = a.stdout.Close()
		if a.cmd.Process != nil {
			_ = a.cmd.Process.Kill()
		}
		if err := a.cmd.Wait(); err != nil && !isKilled(err) {
			message := strings.TrimSpace(a.stderr.String())
			if message != "" {
				a.closeErr = fmt.Errorf("%w: %s", err, truncate(message, 500))
			} else {
				a.closeErr = err
			}
		}
	})
	return a.closeErr
}

// AudioPatch is one scored window of audio.
type AudioPatch struct {
	// Start is the patch's beginning in seconds.
	Start float64
	// Samples is WindowSamples values in [-1, 1).
	Samples []float32
}

// WindowSamples is how many samples one patch holds.
func WindowSamples() int { return int(AudioWindowSeconds * AudioSampleRate) }

// HopSamples is the step between patches.
func HopSamples() int { return int(AudioHopSeconds * AudioSampleRate) }

// ReadPatches decodes a file's audio into overlapping windows.
//
// The whole track is buffered rather than streamed patch by patch: at 16 kHz
// mono a three-hour file is 350 MB of float32, which is a lot but bounded, and
// the alternative is a sliding window whose bookkeeping is where off-by-one
// errors live. Callers with a memory budget should analyse in segments.
func ReadPatches(ctx context.Context, ffmpegPath, path string, maxSeconds float64) ([]AudioPatch, error) {
	source, err := OpenAudio(ctx, ffmpegPath, path)
	if err != nil {
		return nil, err
	}
	defer source.Close()

	limit := 0
	if maxSeconds > 0 {
		limit = int(maxSeconds * AudioSampleRate)
	}

	var samples []float32
	buf := make([]float32, 1<<16)

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		n, err := source.Read(buf)
		if n > 0 {
			samples = append(samples, buf[:n]...)
		}
		if err != nil || n < len(buf) {
			break
		}
		if limit > 0 && len(samples) >= limit {
			samples = samples[:limit]
			break
		}
	}

	if len(samples) == 0 {
		return nil, ErrNoAudio
	}

	return FramePatches(samples), nil
}

// FramePatches splits samples into overlapping windows.
//
// A trailing partial window is ZERO-PADDED rather than dropped: the end of a
// scene is often where the distinctive audio is, and discarding up to a second
// of it to avoid a short buffer would be the wrong trade.
func FramePatches(samples []float32) []AudioPatch {
	window := WindowSamples()
	hop := HopSamples()

	if len(samples) == 0 || window <= 0 || hop <= 0 {
		return nil
	}

	var patches []AudioPatch
	for start := 0; start < len(samples); start += hop {
		patch := AudioPatch{
			Start:   float64(start) / AudioSampleRate,
			Samples: make([]float32, window),
		}

		copied := copy(patch.Samples, samples[start:])
		patches = append(patches, patch)

		// The last window that contains real audio is the last one worth
		// scoring; further windows would be pure padding.
		if copied < window {
			break
		}
	}
	return patches
}

// ScorePatches runs a scorer over patches and returns per-patch detections.
//
// The scorer is injected rather than being a model this owns, which keeps the
// framing above - where the off-by-one errors live - testable without weights,
// and lets the same collapse serve YAMNet or anything replacing it.
func ScorePatches(patches []AudioPatch, score func(patch AudioPatch) ([]float32, error), labels []string, threshold float32) ([]AudioDetection, error) {
	var out []AudioDetection

	for _, patch := range patches {
		scores, err := score(patch)
		if err != nil {
			return nil, err
		}
		if len(scores) != len(labels) {
			return nil, fmt.Errorf("model returned %d scores for %d labels", len(scores), len(labels))
		}

		for i, value := range scores {
			if value < threshold {
				continue
			}
			out = append(out, AudioDetection{
				Label:      labels[i],
				Start:      patch.Start,
				End:        patch.Start + AudioWindowSeconds,
				Confidence: value,
			})
		}
	}
	return out, nil
}

// AudioDetection is one label firing on one patch.
type AudioDetection struct {
	Label      string
	Start      float64
	End        float64
	Confidence float32
}
