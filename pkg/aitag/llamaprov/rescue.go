package llamaprov

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/native"
)

func (a *TaxonomyAnalyzer) rescueOneFrameLabels(
	ctx context.Context,
	videoPath string,
	opts aitag.Options,
	interval float64,
	supports []LabelSupport,
	sampledTimes []float64,
	threshold int,
) (map[string]bool, error) {
	rescued := make(map[string]bool)
	if len(sampledTimes) == 0 {
		return rescued, nil
	}

	labelsByTime := make(map[float64][]string)
	for _, support := range supports {
		if support.Frames != 1 || support.Frames >= threshold {
			continue
		}
		center := sort.SearchFloat64s(sampledTimes, support.FirstAt)
		if center == len(sampledTimes) || (center > 0 && math.Abs(sampledTimes[center-1]-support.FirstAt) < math.Abs(sampledTimes[center]-support.FirstAt)) {
			center--
		}
		for offset := -2; offset <= 2; offset++ {
			index := center + offset
			if index >= 0 && index < len(sampledTimes) {
				at := sampledTimes[index]
				labelsByTime[at] = append(labelsByTime[at], support.Tag)
			}
		}
	}
	if len(labelsByTime) == 0 {
		return rescued, nil
	}

	frames, err := native.OpenFrames(ctx, a.Provider.ffmpegPath, videoPath, native.ExtractOptions{
		Interval: interval,
		Size:     frameSize,
		VR:       opts.VR,
	})
	if err != nil {
		return nil, err
	}
	defer frames.Close()

	var buffer []byte
	for {
		frame, err := frames.Next(buffer)
		if err != nil {
			return nil, err
		}
		if frame == nil {
			break
		}
		buffer = frame.RGB
		labels := labelsAtSampleTime(labelsByTime, frame.Time, interval)
		for _, label := range labels {
			if rescued[label] {
				continue
			}
			decisions, err := a.Provider.Verify(ctx, aitag.Frame{
				RGB: frame.RGB, Width: frames.Width, Height: frames.Height, Time: frame.Time,
			}, []string{label})
			if err != nil {
				return nil, fmt.Errorf("rescue verify %q at %.3f: %w", label, frame.Time, err)
			}
			if decisions[label] {
				rescued[label] = true
			}
		}
	}
	if err := frames.Finish(); err != nil {
		return nil, err
	}
	return rescued, nil
}

func labelsAtSampleTime(labelsByTime map[float64][]string, frameTime, interval float64) []string {
	const minimumTolerance = 0.001
	tolerance := max(minimumTolerance, interval/1000)
	for at, labels := range labelsByTime {
		if math.Abs(at-frameTime) <= tolerance {
			return labels
		}
	}
	return nil
}
