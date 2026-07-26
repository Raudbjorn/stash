// Package generate provides pure helpers for composing gallery contact-sheet
// covers. Decode and resize I/O lives in the task layer.
package generate

import (
	"image"
	"image/color"
	"image/draw"
	"math"
)

const (
	ContactSheetMaxImages   = 12
	ContactSheetCanvasWidth = 2000
	ContactSheetJPEGQuality = 85
	ContactSheetFileExt     = ".jpg"

	// Source aspects (w/h) are clamped into this band before layout. Without a
	// lower bound a long-strip/webtoon page (aspect ~0.05) drives rowH — and the
	// total canvas height — into the tens of thousands: a multi-hundred-MB NRGBA
	// canvas, and past 65535px a silently corrupt JPEG (jpeg.Encode writes frame
	// height as a uint16). With the band, canvasH stays well under ~8000px.
	minCellAspect = 0.25
	maxCellAspect = 4.0
)

var ContactSheetBG = color.NRGBA{R: 26, G: 26, B: 46, A: 255}

// gridByCount[n-1] = {cols, rows} for a source set of size n. Chosen to keep
// the aspect roughly square while minimising blank cells.
var gridByCount = [ContactSheetMaxImages][2]int{
	{1, 1}, // 1
	{2, 1}, // 2
	{3, 1}, // 3
	{2, 2}, // 4
	{3, 2}, // 5 (1 blank)
	{3, 2}, // 6
	{4, 2}, // 7 (1 blank)
	{4, 2}, // 8
	{3, 3}, // 9
	{4, 3}, // 10 (2 blanks)
	{4, 3}, // 11 (1 blank)
	{4, 3}, // 12
}

func gridFor(n int) (cols, rows int) {
	if n < 1 || n > ContactSheetMaxImages {
		return 4, 3
	}
	g := gridByCount[n-1]
	return g[0], g[1]
}

type CellRect struct {
	X, Y, W, H int
}

// PickEvenIndices returns up to ContactSheetMaxImages evenly-spaced indices
// from [0, n) using floor(i*n/max).
func PickEvenIndices(n int) []int {
	if n <= 0 {
		return nil
	}
	if n <= ContactSheetMaxImages {
		out := make([]int, n)
		for i := 0; i < n; i++ {
			out[i] = i
		}
		return out
	}
	out := make([]int, ContactSheetMaxImages)
	for i := 0; i < ContactSheetMaxImages; i++ {
		out[i] = int(math.Floor(float64(i) * float64(n) / float64(ContactSheetMaxImages)))
	}
	return out
}

// ComputeCellGeometry lays out cells with per-row rowH derived from the sum
// of source aspects padded by avgAspect for any blanks, snapping the last
// cell of every full row to canvasW to absorb rounding.
func ComputeCellGeometry(aspects []float64) (cells []CellRect, canvasH int) {
	n := len(aspects)
	if n == 0 {
		return nil, 0
	}

	// Clamp into [minCellAspect, maxCellAspect] before any layout math. This
	// also folds in non-positive / NaN aspects from a failed decode. Copy so we
	// never mutate the caller's slice.
	clamped := make([]float64, n)
	for i, a := range aspects {
		if math.IsNaN(a) || a < minCellAspect {
			a = minCellAspect
		} else if a > maxCellAspect {
			a = maxCellAspect
		}
		clamped[i] = a
	}
	aspects = clamped

	cols, rows := gridFor(n)

	var total float64
	for _, a := range aspects {
		total += a
	}
	avgAspect := total / float64(n)
	if avgAspect <= 0 {
		avgAspect = 1
	}

	cells = make([]CellRect, n)

	yCursor := 0
	for r := 0; r < rows; r++ {
		start := r * cols
		end := start + cols
		if end > n {
			end = n
		}
		if start >= end {
			break
		}
		rowAspects := aspects[start:end]
		blanks := cols - len(rowAspects)

		var sum float64
		for _, a := range rowAspects {
			sum += a
		}
		effectiveSum := sum + float64(blanks)*avgAspect
		if effectiveSum <= 0 {
			effectiveSum = 1
		}
		rowH := int(math.Round(float64(ContactSheetCanvasWidth) / effectiveSum))
		if rowH < 1 {
			rowH = 1
		}

		x := 0
		for i, a := range rowAspects {
			isLastInFullRow := blanks == 0 && i == len(rowAspects)-1
			var right int
			if isLastInFullRow {
				right = ContactSheetCanvasWidth
			} else {
				right = int(math.Round(float64(x) + float64(rowH)*a))
			}
			if right <= x {
				right = x + 1
			}
			if right > ContactSheetCanvasWidth {
				right = ContactSheetCanvasWidth
			}
			cells[start+i] = CellRect{X: x, Y: yCursor, W: right - x, H: rowH}
			x = right
		}
		yCursor += rowH
	}
	canvasH = yCursor
	return cells, canvasH
}

// Compose paints preResized cells onto a single canvas. Cells with a nil
// entry are left as background, so callers can pass a solid-fill placeholder
// on decode failure without aborting the whole sheet.
func Compose(cells []CellRect, preResized []image.Image, canvasH int) image.Image {
	canvas := image.NewNRGBA(image.Rect(0, 0, ContactSheetCanvasWidth, canvasH))
	draw.Draw(canvas, canvas.Bounds(), &image.Uniform{C: ContactSheetBG}, image.Point{}, draw.Src)
	for i, c := range cells {
		if i >= len(preResized) || preResized[i] == nil {
			continue
		}
		dst := image.Rect(c.X, c.Y, c.X+c.W, c.Y+c.H)
		draw.Draw(canvas, dst, preResized[i], image.Point{}, draw.Src)
	}
	return canvas
}
