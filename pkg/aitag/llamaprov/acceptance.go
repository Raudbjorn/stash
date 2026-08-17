package llamaprov

import (
	"math"

	"github.com/stashapp/stash/pkg/aitag"
)

// AcceptMode controls whether low-support scene labels are reported only,
// rescued with surrounding frames, or removed.
type AcceptMode int

const (
	AcceptShadow AcceptMode = iota
	AcceptRescue
	AcceptStrict
)

func (m AcceptMode) String() string {
	switch m {
	case AcceptRescue:
		return "rescue"
	case AcceptStrict:
		return "strict"
	default:
		return "shadow"
	}
}

// ParseAcceptMode accepts the persisted configuration spelling.
func ParseAcceptMode(value string) AcceptMode {
	switch value {
	case "rescue":
		return AcceptRescue
	case "strict":
		return AcceptStrict
	default:
		return AcceptShadow
	}
}

func supportThreshold(minSupport int, fraction float64, sampledFrames int) int {
	if minSupport <= 0 {
		minSupport = 2
	}
	if fraction > 0 {
		minSupport = max(minSupport, int(math.Ceil(float64(sampledFrames)*fraction)))
	}
	return minSupport
}

func applySupportGate(
	spans aitag.SpansByCategory,
	supports []LabelSupport,
	mode AcceptMode,
	minSupport int,
	fraction float64,
	sampledFrames int,
	rescued map[string]bool,
) (aitag.SpansByCategory, []LabelSupport, []LabelSupport) {
	threshold := supportThreshold(minSupport, fraction, sampledFrames)
	accepted := make(map[string]bool, len(supports))
	kept := make([]LabelSupport, 0, len(supports))
	dropped := make([]LabelSupport, 0)
	for _, support := range supports {
		accept := support.Frames >= threshold || (mode == AcceptRescue && support.Frames == 1 && rescued[support.Tag])
		accepted[support.Tag] = accept
		if accept {
			kept = append(kept, support)
		} else {
			dropped = append(dropped, support)
		}
	}
	if mode == AcceptShadow {
		return spans, kept, dropped
	}

	filtered := make(aitag.SpansByCategory, len(spans))
	for category, byTag := range spans {
		labels := make(aitag.SpansByTag)
		for tag, tagSpans := range byTag {
			if accepted[tag] {
				labels[tag] = tagSpans
			}
		}
		if len(labels) > 0 {
			filtered[category] = labels
		}
	}
	return filtered, kept, dropped
}

func supportsToMetrics(supports []LabelSupport) []map[string]any {
	ret := make([]map[string]any, len(supports))
	for i, support := range supports {
		ret[i] = map[string]any{
			"tag":        support.Tag,
			"stash_id":   support.StashID,
			"frames":     support.Frames,
			"span_count": support.SpanCount,
			"first_at":   support.FirstAt,
			"last_at":    support.LastAt,
		}
	}
	return ret
}
