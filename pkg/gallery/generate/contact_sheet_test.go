package generate

import (
	"image"
	"testing"
)

func TestPickEvenIndices(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want []int
	}{
		{"single", 1, []int{0}},
		{"exactly twelve", 12, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
		{"twenty-four even strides", 24, []int{0, 2, 4, 6, 8, 10, 12, 14, 16, 18, 20, 22}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PickEvenIndices(tt.n)
			if len(got) != len(tt.want) {
				t.Fatalf("PickEvenIndices(%d) len = %d, want %d (got=%v)", tt.n, len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("PickEvenIndices(%d)[%d] = %d, want %d", tt.n, i, got[i], tt.want[i])
				}
			}
		})
	}

	// Thirteen: returns exactly 12 entries, starting at index 0.
	t.Run("thirteen", func(t *testing.T) {
		got := PickEvenIndices(13)
		if len(got) != ContactSheetMaxImages {
			t.Fatalf("PickEvenIndices(13) len = %d, want %d", len(got), ContactSheetMaxImages)
		}
		if got[0] != 0 {
			t.Fatalf("PickEvenIndices(13)[0] = %d, want 0", got[0])
		}
		// Monotonically non-decreasing, all within [0, 13).
		for i := 1; i < len(got); i++ {
			if got[i] < got[i-1] {
				t.Fatalf("PickEvenIndices(13) not monotonic at %d: %v", i, got)
			}
			if got[i] >= 13 {
				t.Fatalf("PickEvenIndices(13)[%d]=%d out of range", i, got[i])
			}
		}
	})

	t.Run("zero", func(t *testing.T) {
		if got := PickEvenIndices(0); got != nil {
			t.Fatalf("PickEvenIndices(0) = %v, want nil", got)
		}
	})
}

func TestComputeCellGeometry(t *testing.T) {
	// Mix of wide, square, tall, and ultrawide images.
	mixedAspects := func(n int) []float64 {
		pool := []float64{1.5, 1.0, 0.66, 2.4, 1.77, 0.8, 1.33, 1.0, 0.9, 2.0, 1.6, 0.5}
		out := make([]float64, n)
		for i := 0; i < n; i++ {
			out[i] = pool[i%len(pool)]
		}
		return out
	}

	for _, n := range []int{2, 6, 12} {
		n := n
		t.Run("", func(t *testing.T) {
			aspects := mixedAspects(n)
			cells, canvasH := ComputeCellGeometry(aspects)

			if len(cells) != n {
				t.Fatalf("N=%d: expected %d cells, got %d", n, n, len(cells))
			}
			if canvasH <= 0 {
				t.Fatalf("N=%d: canvasH = %d, want >0", n, canvasH)
			}

			cols, _ := gridFor(n)

			rowHSum := 0
			seenRows := 0
			for r := 0; r*cols < n; r++ {
				start := r * cols
				end := start + cols
				if end > n {
					end = n
				}
				rowCells := cells[start:end]
				blanks := cols - len(rowCells)

				// All cells in a row share Y and H.
				y0, h0 := rowCells[0].Y, rowCells[0].H
				for i, c := range rowCells {
					if c.Y != y0 {
						t.Fatalf("N=%d row=%d cell=%d: Y=%d, want %d", n, r, i, c.Y, y0)
					}
					if c.H != h0 {
						t.Fatalf("N=%d row=%d cell=%d: H=%d, want %d", n, r, i, c.H, h0)
					}
					if c.W <= 0 {
						t.Fatalf("N=%d row=%d cell=%d: zero/neg width %d", n, r, i, c.W)
					}
					if c.X < 0 || c.X+c.W > ContactSheetCanvasWidth {
						t.Fatalf("N=%d row=%d cell=%d: out-of-bounds X=%d W=%d", n, r, i, c.X, c.W)
					}
				}

				// Last cell of a FULL row snaps exactly to canvas right edge.
				last := rowCells[len(rowCells)-1]
				if blanks == 0 && last.X+last.W != ContactSheetCanvasWidth {
					t.Fatalf("N=%d row=%d: full-row last cell right-edge=%d, want %d",
						n, r, last.X+last.W, ContactSheetCanvasWidth)
				}

				rowHSum += h0
				seenRows++
			}

			// canvasH must equal the sum of per-row heights.
			if rowHSum != canvasH {
				t.Fatalf("N=%d: sum(rowH)=%d, canvasH=%d", n, rowHSum, canvasH)
			}
		})
	}
}

func TestCompose(t *testing.T) {
	aspects := []float64{1.5, 1.0, 0.66, 1.77, 1.33, 0.9}
	cells, canvasH := ComputeCellGeometry(aspects)

	pre := make([]image.Image, len(cells))
	for i, c := range cells {
		pre[i] = image.NewRGBA(image.Rect(0, 0, c.W, c.H))
	}

	got := Compose(cells, pre, canvasH)
	b := got.Bounds()
	if b.Dx() != ContactSheetCanvasWidth {
		t.Fatalf("width = %d, want %d", b.Dx(), ContactSheetCanvasWidth)
	}
	if b.Dy() != canvasH {
		t.Fatalf("height = %d, want %d", b.Dy(), canvasH)
	}
}
