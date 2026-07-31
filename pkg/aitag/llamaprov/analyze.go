package llamaprov

import (
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"sort"
	"time"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/native"
	_ "golang.org/x/image/webp"
)

const frameSize = 512

func (p *Provider) AnalyzeVideo(ctx context.Context, path string, opts aitag.Options, sink aitag.Sink) (*aitag.Result, error) {
	if p.waitReady != nil {
		if err := p.waitReady(ctx); err != nil {
			return nil, err
		}
	}
	started := time.Now()
	interval := opts.FrameInterval
	if interval <= 0 {
		interval = p.interval
	}
	duration, err := native.ProbeDuration(ctx, p.ffmpegPath, path)
	if err != nil {
		duration = 0
	}
	frames, err := native.OpenFrames(ctx, p.ffmpegPath, path, native.ExtractOptions{
		Interval: interval,
		Size:     frameSize,
		VR:       opts.VR,
	})
	if err != nil {
		return nil, err
	}
	defer frames.Close()

	detected := make([]aitag.Frame, 0)
	var buffer []byte
	var lastTime float64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame, err := frames.Next(buffer)
		if err != nil {
			return nil, err
		}
		if frame == nil {
			break
		}
		buffer = frame.RGB
		lastTime = frame.Time
		decisions, err := p.classifyFrame(ctx, frame.RGB, frames.Width, frames.Height)
		if err != nil {
			return nil, err
		}
		hits := make([]aitag.Detection, 0, len(p.labels))
		for _, label := range p.labels {
			if decisions[label] {
				hits = append(hits, aitag.Detection{Tag: label})
			}
		}
		detected = append(detected, aitag.Frame{
			Index:  frame.Time,
			Labels: map[string][]aitag.Detection{p.category: hits},
		})
		fraction := -1.0
		if duration > 0 {
			fraction = min((frame.Time+interval)/duration, 1)
		}
		sink.Report(aitag.Progress{
			Fraction: fraction,
			Frames:   frames.Count(),
			Message:  fmt.Sprintf("Classified %d frames", frames.Count()),
		})
	}
	if err := frames.Finish(); err != nil {
		return nil, err
	}
	if frames.Count() == 0 {
		return nil, native.ErrNoFrames
	}
	if duration == 0 {
		duration = lastTime + interval
	}
	spans := aitag.CollapseFrames(detected, interval, p.maxMerge)
	aitag.SortSpans(spans)
	return &aitag.Result{
		SchemaVersion: 3,
		Duration:      duration,
		FrameInterval: interval,
		Models:        []aitag.ModelInfo{p.modelInfo(interval)},
		Spans:         spans,
		Metrics: map[string]any{
			"frames":        frames.Count(),
			"total_seconds": time.Since(started).Seconds(),
		},
	}, nil
}

func (p *Provider) AnalyzeImages(ctx context.Context, paths []string, _ aitag.Options) (*aitag.ImageResult, error) {
	if p.waitReady != nil {
		if err := p.waitReady(ctx); err != nil {
			return nil, err
		}
	}
	out := &aitag.ImageResult{
		Tags:   make(map[string]map[string][]string, len(paths)),
		Models: []aitag.ModelInfo{p.modelInfo(p.interval)},
		Errors: make(map[string]string),
	}
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rgb, width, height, err := readImageRGB(path)
		if err != nil {
			out.Errors[path] = err.Error()
			continue
		}
		decisions, err := p.classifyFrame(ctx, rgb, width, height)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			out.Errors[path] = err.Error()
			continue
		}
		labels := make([]string, 0, len(p.labels))
		for _, label := range p.labels {
			if decisions[label] {
				labels = append(labels, label)
			}
		}
		sort.Strings(labels)
		out.Tags[path] = map[string][]string{p.category: labels}
	}
	if len(out.Errors) == 0 {
		out.Errors = nil
	}
	return out, nil
}

func readImageRGB(path string) ([]byte, int, int, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		return nil, 0, 0, err
	}
	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, 0, 0, fmt.Errorf("image has invalid dimensions %dx%d", width, height)
	}
	rgb := make([]byte, width*height*3)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			r, g, b, _ := decoded.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			offset := (y*width + x) * 3
			rgb[offset] = byte(r >> 8)
			rgb[offset+1] = byte(g >> 8)
			rgb[offset+2] = byte(b >> 8)
		}
	}
	return rgb, width, height, nil
}
