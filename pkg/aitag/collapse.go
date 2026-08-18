package aitag

import "sort"

// Stage one: per-frame detections collapsed into raw spans.
//
// This is a faithful port of AI_VideoResult._mutate_server_result_tags, and it
// is what goes into the database. The native provider runs it over its own
// frames so both providers store the same shape; the HTTP provider gets spans
// already collapsed by the remote server and does not run it.

// Frame is one sampled frame's detections.
type Frame struct {
	// Index is the frame's timestamp in seconds. Named for the field the wire
	// format uses ("frame_index"), which is a seconds value despite the name.
	Index float64
	// Labels maps a category to the labels that fired on this frame.
	Labels map[string][]Detection
	// RGB, Width, Height, and Time are populated when the frame is passed to
	// an open-vocabulary classifier. Detection-only frames leave them zero.
	RGB           []byte
	Width, Height int
	Time          float64
}

// Detection is one label on one frame.
type Detection struct {
	Tag string
	// Confidence is nil when the model reported only a threshold decision.
	Confidence *float64
}

// CollapseFrames turns per-frame detections into raw spans per category and tag.
//
// The merge rule is ported exactly, including a trap worth stating plainly: a
// span is extended only when the incoming confidence is EXACTLY EQUAL to the
// running span's. With real confidences that almost never holds, so every frame
// becomes its own span; the upstream server avoids it by either omitting
// confidences or rounding them to two decimals. Emitting raw confidences here
// without quantizing would silently multiply the span count by the frame count
// - a change that looks like a data explosion rather than a bug.
//
// maxMergeSeconds is the gap tolerated when joining consecutive detections. The
// upstream default is 2 but the shipped configuration overrides it to 4, so
// callers should pass the configured value rather than relying on a default.
func CollapseFrames(frames []Frame, frameInterval, maxMergeSeconds float64) SpansByCategory {
	out := SpansByCategory{}

	// Frames must be in time order for the merge to mean anything; a provider
	// that produced them concurrently may not have kept them sorted.
	ordered := make([]Frame, len(frames))
	copy(ordered, frames)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })

	for _, frame := range ordered {
		for category, detections := range frame.Labels {
			byTag, ok := out[category]
			if !ok {
				byTag = SpansByTag{}
				out[category] = byTag
			}

			for _, detection := range detections {
				spans := byTag[detection.Tag]
				if len(spans) == 0 {
					byTag[detection.Tag] = []Span{{Start: frame.Index, Confidence: detection.Confidence}}
					continue
				}

				last := &spans[len(spans)-1]

				// The reference measures the gap from the span's END when it has
				// one and from its START when it does not - not the same thing,
				// and preserved rather than tidied.
				var reference float64
				if last.End == nil {
					reference = last.Start
				} else {
					reference = *last.End
				}

				gap := frame.Index - reference - frameInterval
				if gap <= maxMergeSeconds && sameConfidence(last.Confidence, detection.Confidence) {
					end := frame.Index
					last.End = &end
					continue
				}

				byTag[detection.Tag] = append(spans, Span{Start: frame.Index, Confidence: detection.Confidence})
			}
		}
	}

	return out
}

// sameConfidence reproduces Python's `a == b` over two optional floats.
//
// None == None is true, None == 0.5 is false, and two floats compare exactly.
// The exactness is the point: see CollapseFrames.
func sameConfidence(a, b *float64) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

// QuantizeConfidence rounds to the precision the upstream server uses.
//
// Two decimals is not arbitrary: the reference rounds there before emitting a
// confidence, which is the only reason the exact-equality merge above ever
// joins two frames. A native provider that wants merging must quantize to the
// same precision, and one that wants per-frame spans should emit nil instead.
func QuantizeConfidence(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// SortSpans puts every tag's spans in time order.
//
// Downstream clustering walks spans in order and merges neighbours; out-of-order
// input would silently produce overlapping markers rather than an error.
func SortSpans(spans SpansByCategory) {
	for _, byTag := range spans {
		for tag := range byTag {
			list := byTag[tag]
			sort.SliceStable(list, func(i, j int) bool { return list[i].Start < list[j].Start })
			byTag[tag] = list
		}
	}
}

// CountSpans totals the spans across every category, for logging.
func CountSpans(spans SpansByCategory) int {
	total := 0
	for _, byTag := range spans {
		for _, list := range byTag {
			total += len(list)
		}
	}
	return total
}
