package transcodebenefit

import (
	"sort"

	"github.com/stashapp/stash/pkg/models"
)

// MinSide returns the smaller of width and height.
func MinSide(width, height int) int {
	if width < height {
		return width
	}
	return height
}

// SizeP80 returns the 80th-percentile size among primary video files.
// Empty libraries or n < 5 return 0 so the quintile never fires.
func SizeP80(sizes []int64) int64 {
	n := len(sizes)
	if n < 5 {
		return 0
	}
	sorted := append([]int64(nil), sizes...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return sorted[int(0.8*float64(n))]
}

// Level scores transcode benefit. First matching rule wins.
func Level(minSide int, size int64, sizeP80 int64, hwDest bool) models.TranscodeBenefitEnum {
	if minSide <= 480 {
		return models.TranscodeBenefitLow
	}
	if minSide >= 1080 || (sizeP80 > 0 && size >= sizeP80) {
		return models.TranscodeBenefitHigh
	}
	if hwDest {
		return models.TranscodeBenefitMedium
	}
	return models.TranscodeBenefitLow
}
