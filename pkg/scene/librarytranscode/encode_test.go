package librarytranscode

import "testing"

func TestMinSideScaleFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		target int
		w, h   int
		cuda   bool
		want   string
	}{
		{name: "landscape software", target: 480, w: 1920, h: 1080, want: "scale=-2:480"},
		{name: "landscape cuda", target: 480, w: 1920, h: 1080, cuda: true, want: "scale_cuda=-2:480"},
		{name: "portrait software", target: 480, w: 1080, h: 1920, want: "scale=480:-2"},
		{name: "portrait cuda", target: 360, w: 1080, h: 1920, cuda: true, want: "scale_cuda=360:-2"},
		{name: "square", target: 480, w: 1080, h: 1080, want: "scale=-2:480"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MinSideScaleFilter(tt.target, tt.w, tt.h, tt.cuda)
			if got != tt.want {
				t.Fatalf("MinSideScaleFilter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNVENCSpecUsesMinSideAndOutputRate(t *testing.T) {
	t.Parallel()

	portrait := NVENCSpec(360, 35, "48k", 24, 1080, 1920)
	if portrait.ScaleFilter != "scale_cuda=360:-2" {
		t.Fatalf("portrait scale = %q", portrait.ScaleFilter)
	}
	if portrait.FrameRate != 24 {
		t.Fatalf("FrameRate = %d, want 24", portrait.FrameRate)
	}
	if !portrait.UseCUDA {
		t.Fatal("expected CUDA")
	}

	land := NVENCSpec(480, 24, "96k", 0, 1920, 1080)
	if land.ScaleFilter != "scale_cuda=-2:480" {
		t.Fatalf("landscape scale = %q", land.ScaleFilter)
	}
	if land.FrameRate != 0 {
		t.Fatalf("HQ FrameRate = %d, want 0", land.FrameRate)
	}
}
