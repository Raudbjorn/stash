package transcodebenefit

import (
	"testing"

	"github.com/stashapp/stash/pkg/models"
)

func TestLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		minSide int
		size    int64
		sizeP80 int64
		hwDest  bool
		want    models.TranscodeBenefitEnum
	}{
		{name: "1080p any size", minSide: 1080, size: 1, sizeP80: 100, hwDest: false, want: models.TranscodeBenefitHigh},
		{name: "2160p", minSide: 2160, size: 1, sizeP80: 0, hwDest: false, want: models.TranscodeBenefitHigh},
		{name: "360p even huge", minSide: 360, size: 1 << 40, sizeP80: 1, hwDest: true, want: models.TranscodeBenefitLow},
		{name: "480p", minSide: 480, size: 1 << 40, sizeP80: 1, hwDest: true, want: models.TranscodeBenefitLow},
		{name: "720p size>=p80", minSide: 720, size: 100, sizeP80: 100, hwDest: false, want: models.TranscodeBenefitHigh},
		{name: "720p size<p80 hwDest true", minSide: 720, size: 50, sizeP80: 100, hwDest: true, want: models.TranscodeBenefitMedium},
		{name: "720p size<p80 hwDest false", minSide: 720, size: 50, sizeP80: 100, hwDest: false, want: models.TranscodeBenefitLow},
		{name: "p80=0 720p hw", minSide: 720, size: 1 << 40, sizeP80: 0, hwDest: true, want: models.TranscodeBenefitMedium},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Level(tt.minSide, tt.size, tt.sizeP80, tt.hwDest)
			if got != tt.want {
				t.Fatalf("Level() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestMinSide(t *testing.T) {
	t.Parallel()
	if got := MinSide(1920, 1080); got != 1080 {
		t.Fatalf("MinSide(1920, 1080) = %d, want 1080", got)
	}
	if got := MinSide(1080, 1920); got != 1080 {
		t.Fatalf("MinSide(1080, 1920) = %d, want 1080", got)
	}
}

func TestSizeP80(t *testing.T) {
	t.Parallel()
	if got := SizeP80(nil); got != 0 {
		t.Fatalf("SizeP80(nil) = %d, want 0", got)
	}
	if got := SizeP80([]int64{1, 2, 3, 4}); got != 0 {
		t.Fatalf("SizeP80(n=4) = %d, want 0", got)
	}
	sizes := []int64{10, 20, 30, 40, 50}
	if got := SizeP80(sizes); got != 50 {
		t.Fatalf("SizeP80(n=5) = %d, want 50", got)
	}
}
