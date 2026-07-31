package native

import (
	"fmt"
	"math"
	"sort"
)

// Face detection and embedding.
//
// Two models: YuNet finds faces and their landmarks, SFace turns a cropped
// face into a 128-d vector. Both are small enough to run alongside the frame
// embedder without changing the cost profile.
//
// InsightFace's buffalo_l is deliberately not used. Its code is MIT but its
// weights are licensed for research only, which is not a free licence - see the
// catalog. YuNet (MIT) and SFace (Apache-2.0) fill the same role.

// Box is an axis-aligned rectangle in pixels.
type Box struct {
	X, Y, W, H float32
}

// Right and Bottom are the far edges.
func (b Box) Right() float32  { return b.X + b.W }
func (b Box) Bottom() float32 { return b.Y + b.H }

// Area is the box's area, clamped at zero for a degenerate box.
func (b Box) Area() float32 {
	if b.W <= 0 || b.H <= 0 {
		return 0
	}
	return b.W * b.H
}

// IntersectionOverUnion measures overlap, which is what NMS thresholds on.
func (b Box) IntersectionOverUnion(other Box) float32 {
	x1 := maxFloat(b.X, other.X)
	y1 := maxFloat(b.Y, other.Y)
	x2 := minFloat(b.Right(), other.Right())
	y2 := minFloat(b.Bottom(), other.Bottom())

	if x2 <= x1 || y2 <= y1 {
		return 0
	}

	intersection := (x2 - x1) * (y2 - y1)
	union := b.Area() + other.Area() - intersection
	if union <= 0 {
		return 0
	}
	return intersection / union
}

// FaceDetection is one detected face.
type FaceDetection struct {
	Box   Box
	Score float32
	// Landmarks are the five points YuNet reports: right eye, left eye, nose,
	// right mouth corner, left mouth corner. Used to align the crop, which is
	// what makes SFace's embeddings comparable across poses.
	Landmarks [5][2]float32
}

// NonMaxSuppression keeps the highest-scoring box from each overlapping cluster.
//
// A detector fires several times on one face at slightly different scales;
// without this every face becomes three or four, and the downstream grouping
// then has to work out that they are the same person.
func NonMaxSuppression(detections []FaceDetection, iouThreshold float32) []FaceDetection {
	if len(detections) == 0 {
		return nil
	}

	ordered := make([]FaceDetection, len(detections))
	copy(ordered, detections)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Score > ordered[j].Score })

	var kept []FaceDetection
	suppressed := make([]bool, len(ordered))

	for i := range ordered {
		if suppressed[i] {
			continue
		}
		kept = append(kept, ordered[i])

		for j := i + 1; j < len(ordered); j++ {
			if suppressed[j] {
				continue
			}
			if ordered[i].Box.IntersectionOverUnion(ordered[j].Box) > iouThreshold {
				suppressed[j] = true
			}
		}
	}
	return kept
}

// DecodeYuNet turns YuNet's raw outputs into detections.
//
// YuNet predicts on a grid at each of three strides, with one anchor per cell.
// For each cell it emits a classification score, an objectness score, a box
// offset and five landmark offsets, all relative to the cell.
//
// The score is the PRODUCT of the two heads, not either alone: the model was
// trained that way, and using one gives detections that look reasonable and
// threshold quite differently.
func DecodeYuNet(cls, obj, bbox, kps []float32, stride, inputW, inputH int, threshold float32) ([]FaceDetection, error) {
	cols := inputW / stride
	rows := inputH / stride
	cells := cols * rows

	if len(cls) < cells || len(obj) < cells {
		return nil, fmt.Errorf("stride %d: expected %d cells, got %d cls and %d obj scores",
			stride, cells, len(cls), len(obj))
	}
	if len(bbox) < cells*4 {
		return nil, fmt.Errorf("stride %d: expected %d box values, got %d", stride, cells*4, len(bbox))
	}
	if len(kps) < cells*10 {
		return nil, fmt.Errorf("stride %d: expected %d landmark values, got %d", stride, cells*10, len(kps))
	}

	var out []FaceDetection

	for row := 0; row < rows; row++ {
		for col := 0; col < cols; col++ {
			cell := row*cols + col

			score := float32(math.Sqrt(float64(cls[cell] * obj[cell])))
			if score < threshold {
				continue
			}

			// Offsets are in units of the stride, measured from the cell's
			// top-left corner.
			cx := (float32(col) + bbox[cell*4+0]) * float32(stride)
			cy := (float32(row) + bbox[cell*4+1]) * float32(stride)
			w := float32(math.Exp(float64(bbox[cell*4+2]))) * float32(stride)
			h := float32(math.Exp(float64(bbox[cell*4+3]))) * float32(stride)

			detection := FaceDetection{
				// The predicted point is the box's CENTRE; the box itself is
				// expressed by its top-left corner.
				Box:   Box{X: cx - w/2, Y: cy - h/2, W: w, H: h},
				Score: score,
			}

			for point := 0; point < 5; point++ {
				detection.Landmarks[point][0] = (float32(col) + kps[cell*10+point*2+0]) * float32(stride)
				detection.Landmarks[point][1] = (float32(row) + kps[cell*10+point*2+1]) * float32(stride)
			}

			out = append(out, detection)
		}
	}

	return out, nil
}

// ScaleDetections maps detections from the model's input size back to the
// source frame.
//
// The detector runs on a resized frame, so its coordinates mean nothing until
// they are scaled back - and forgetting produces boxes that are plausibly
// shaped and in the wrong place.
func ScaleDetections(detections []FaceDetection, fromW, fromH, toW, toH int) []FaceDetection {
	if fromW == 0 || fromH == 0 {
		return detections
	}

	sx := float32(toW) / float32(fromW)
	sy := float32(toH) / float32(fromH)

	out := make([]FaceDetection, len(detections))
	for i, d := range detections {
		d.Box.X *= sx
		d.Box.Y *= sy
		d.Box.W *= sx
		d.Box.H *= sy
		for point := range d.Landmarks {
			d.Landmarks[point][0] *= sx
			d.Landmarks[point][1] *= sy
		}
		out[i] = d
	}
	return out
}

// CropFace extracts and resizes a face to the recogniser's input size.
//
// Nearest-neighbour rather than bilinear: a face crop is already small, the
// recogniser is robust to it, and a bilinear resample here would be several
// times the cost of the crop for no measurable accuracy.
func CropFace(rgb []byte, width, height int, box Box, size int) ([]byte, error) {
	if size <= 0 {
		return nil, fmt.Errorf("crop size must be positive")
	}
	if len(rgb) != width*height*3 {
		return nil, fmt.Errorf("frame is %d bytes, expected %d", len(rgb), width*height*3)
	}

	// Clamped to the frame: a detector near an edge routinely predicts a box
	// that extends past it, and reading outside the buffer would be a crash
	// rather than a bad crop.
	x0 := clampInt(int(box.X), 0, width-1)
	y0 := clampInt(int(box.Y), 0, height-1)
	x1 := clampInt(int(box.Right()), x0+1, width)
	y1 := clampInt(int(box.Bottom()), y0+1, height)

	cropW := x1 - x0
	cropH := y1 - y0
	if cropW <= 0 || cropH <= 0 {
		return nil, fmt.Errorf("degenerate face box %+v", box)
	}

	out := make([]byte, size*size*3)
	for y := 0; y < size; y++ {
		srcY := y0 + y*cropH/size
		for x := 0; x < size; x++ {
			srcX := x0 + x*cropW/size

			src := (srcY*width + srcX) * 3
			dst := (y*size + x) * 3
			out[dst+0] = rgb[src+0]
			out[dst+1] = rgb[src+1]
			out[dst+2] = rgb[src+2]
		}
	}
	return out, nil
}

// FaceTrack groups detections believed to be the same person.
type FaceTrack struct {
	// Embedding is the running mean of the track's face vectors, normalised.
	Embedding []float32
	// Times are when the face was seen.
	Times []float64
	// Count is how many detections contributed.
	Count int
}

// GroupFaces clusters face embeddings by similarity.
//
// Greedy single-pass agglomeration rather than a proper clustering algorithm:
// faces within one scene are few, and the alternative needs a distance matrix
// and a threshold that is no easier to choose.
func GroupFaces(embeddings [][]float32, times []float64, threshold float32) []FaceTrack {
	var tracks []FaceTrack

	for i, embedding := range embeddings {
		if len(embedding) == 0 {
			continue
		}

		at := 0.0
		if i < len(times) {
			at = times[i]
		}

		best := -1
		bestScore := threshold
		for t := range tracks {
			if score := CosineSimilarity(tracks[t].Embedding, embedding); score > bestScore {
				best = t
				bestScore = score
			}
		}

		if best < 0 {
			vector := make([]float32, len(embedding))
			copy(vector, embedding)
			NormalizeInPlace(vector, len(vector))
			tracks = append(tracks, FaceTrack{Embedding: vector, Times: []float64{at}, Count: 1})
			continue
		}

		// Running mean, renormalised: a track's centroid should represent every
		// view of the face, not just the first one that matched.
		track := &tracks[best]
		weight := float32(track.Count)
		for j := range track.Embedding {
			track.Embedding[j] = (track.Embedding[j]*weight + embedding[j]) / (weight + 1)
		}
		NormalizeInPlace(track.Embedding, len(track.Embedding))
		track.Times = append(track.Times, at)
		track.Count++
	}

	// Largest first: the most-seen face is the one a user cares about.
	sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].Count > tracks[j].Count })
	return tracks
}

func maxFloat(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float32) float32 {
	if a < b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
