package aitag

import "sort"

// Stage two: raw spans clustered into scene markers.
//
// A port of timeframe_processing.compute_video_timespans_clustering. It runs on
// the writeback path rather than the storage path: the raw spans are the record
// and are stored as-is, so re-tuning these parameters regenerates markers
// without re-running inference.

// ClusterParams are the four tuning knobs of the merge decision.
//
// The shipped defaults [0.04, 0.85, 0.25, 0.0] were fitted against the reference
// implementation INCLUDING its merge-ordering quirk (see timeFrame.merge). They
// are not independently meaningful numbers; changing the arithmetic without
// refitting them changes every marker boundary.
type ClusterParams struct {
	DensityWeight float64
	GapFactor     float64
	AverageFactor float64
	MinGap        float64
}

// DefaultClusterParams matches timespan_generation_systems.Clustering.defaults.
func DefaultClusterParams() ClusterParams {
	return ClusterParams{
		DensityWeight: 0.04,
		GapFactor:     0.85,
		AverageFactor: 0.25,
		MinGap:        0.0,
	}
}

// timeFrame is a cluster under construction.
type timeFrame struct {
	start float64
	end   float64
	// totalConfidence accumulates confidence-weighted duration. See merge for
	// why it is not the quantity its name suggests.
	totalConfidence float64
}

func (t *timeFrame) duration(frameInterval float64) float64 {
	return (t.end - t.start) + frameInterval
}

func (t *timeFrame) density(frameInterval float64) float64 {
	d := t.duration(frameInterval)
	if d == 0 {
		return 0
	}
	return t.totalConfidence / d
}

// merge extends a cluster with an adjacent span.
//
// The ordering here is deliberate and is NOT a bug to fix: start and end are
// updated BEFORE the duration used to weight the incoming confidence is
// computed, so the weight is the merged cluster's new duration rather than the
// incoming span's. That makes totalConfidence a growing, order-dependent
// quantity rather than a confidence-weighted total - and the tuned defaults
// above were fitted against exactly this behaviour. "Correcting" it would
// change every marker boundary in every library.
func (t *timeFrame) merge(newStart, newEnd, confidence, frameInterval float64) {
	if newStart < t.start {
		t.start = newStart
	}
	if newEnd > t.end {
		t.end = newEnd
	}
	t.totalConfidence += confidence * t.duration(frameInterval)
}

// Cluster turns raw spans into markers using the configured rules.
//
// Categories with no rules are skipped entirely, as are labels the category
// does not list: a rules file is also the list of what is worth marking.
func Cluster(result *Result, rules *Rules, params ClusterParams) []Marker {
	if result == nil || rules == nil {
		return nil
	}

	frameInterval := result.FrameInterval
	if frameInterval <= 0 {
		frameInterval = 2
	}

	var markers []Marker

	// Deterministic order: markers are written to the database and compared in
	// tests, and map iteration would make both unstable.
	for _, category := range sortedKeys(result.Spans) {
		if !rules.HasCategory(category) {
			continue
		}

		byTag := result.Spans[category]
		for _, tag := range sortedKeys(byTag) {
			rule, ok := rules.Lookup(category, tag)
			if !ok {
				continue
			}

			minDuration := rules.MinMarkerDuration(rule, result.Duration)
			if minDuration <= 0 {
				// Zero means "do not mark this tag", including the case where
				// the value was unparseable.
				continue
			}
			threshold := rules.TagThreshold(rule)

			clusters := clusterTag(byTag[tag], threshold, frameInterval, params)
			for _, c := range clusters {
				if c.duration(frameInterval) < minDuration {
					continue
				}
				markers = append(markers, Marker{
					Category:        category,
					Tag:             tag,
					RenamedTag:      rule.RenamedTag,
					Start:           c.start,
					End:             c.end,
					TotalConfidence: c.totalConfidence,
				})
			}
		}
	}

	sort.SliceStable(markers, func(i, j int) bool {
		if markers[i].Start != markers[j].Start {
			return markers[i].Start < markers[j].Start
		}
		return markers[i].RenamedTag < markers[j].RenamedTag
	})
	return markers
}

// clusterTag builds one label's clusters.
func clusterTag(spans []Span, threshold, frameInterval float64, params ClusterParams) []*timeFrame {
	// Seed buckets: consecutive spans exactly one frame apart are joined, and
	// anything else starts a new bucket.
	var buckets []*timeFrame
	var current *timeFrame

	for _, span := range spans {
		confidence := spanConfidence(span)
		if confidence < threshold {
			continue
		}

		start := span.Start
		end := span.EndOrStart()
		duration := (end - start) + frameInterval

		if current == nil {
			current = &timeFrame{start: start, end: end, totalConfidence: confidence * duration}
			continue
		}

		// Exact equality against the frame interval, as the reference has it: a
		// span that starts any other distance away begins a new bucket and is
		// joined later, or not, by the iterative pass below.
		if start-current.end == frameInterval {
			current.merge(start, end, confidence, frameInterval)
			continue
		}

		buckets = append(buckets, current)
		current = &timeFrame{start: start, end: end, totalConfidence: confidence * duration}
	}
	if current != nil {
		buckets = append(buckets, current)
	}

	return mergeBuckets(buckets, frameInterval, params)
}

// mergeBuckets runs the iterative pairwise merge.
//
// Capped at ten passes exactly as the reference is. The cap is load-bearing:
// the merge condition is not monotonic, so without it a pathological input
// could oscillate rather than converge.
func mergeBuckets(buckets []*timeFrame, frameInterval float64, params ClusterParams) []*timeFrame {
	const maxIterations = 10

	merged := buckets
	for iteration := 0; iteration < maxIterations; iteration++ {
		var next []*timeFrame
		occurred := false

		for i := 0; i < len(merged); {
			if i < len(merged)-1 && shouldMerge(merged[i], merged[i+1], frameInterval, params) {
				next = append(next, &timeFrame{
					start:           merged[i].start,
					end:             merged[i+1].end,
					totalConfidence: merged[i].totalConfidence + merged[i+1].totalConfidence,
				})
				// Skip the absorbed bucket. Note this is pairwise: a run of
				// three mergeable buckets joins two this pass and the third on
				// the next, which is why the loop iterates at all.
				i += 2
				occurred = true
				continue
			}
			next = append(next, merged[i])
			i++
		}

		merged = next
		if !occurred {
			break
		}
	}
	return merged
}

// shouldMerge is the merge decision, ported verbatim.
//
// Two clusters join when the gap between them is small relative to their
// weighted durations - so a long, dense run absorbs a nearby short one, while
// two short bursts far apart stay separate.
func shouldMerge(current, next *timeFrame, frameInterval float64, params ClusterParams) bool {
	gap := next.start - current.end - frameInterval

	durationCurrent := current.duration(frameInterval)
	durationNext := next.duration(frameInterval)

	weightedCurrent := durationCurrent * (1 + params.DensityWeight*current.density(frameInterval))
	weightedNext := durationNext * (1 + params.DensityWeight*next.density(frameInterval))

	weightedDiff := weightedCurrent - weightedNext
	if weightedDiff < 0 {
		weightedDiff = -weightedDiff
	}

	smaller := weightedCurrent
	if weightedNext < smaller {
		smaller = weightedNext
	}

	return gap <= params.MinGap+(smaller+weightedDiff*params.AverageFactor)*params.GapFactor
}

// spanConfidence returns a span's confidence for thresholding.
//
// A span with no confidence counts as passing. That is not a fudge: a provider
// that omits confidences has already applied its own threshold before emitting
// the label at all, so the detection's presence IS the decision. Treating it as
// zero would silently discard every result from the HTTP provider, whose v3
// route does not return confidences.
func spanConfidence(span Span) float64 {
	if span.Confidence == nil {
		return 1
	}
	return *span.Confidence
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TagTotals sums each label's covered seconds, per category.
//
// This is what the aggregates table stores and what the recommenders join on,
// so it is computed from the RAW spans rather than from markers: a label that
// never reaches marker length still contributes to how much of a scene it
// covers.
func TagTotals(result *Result) map[string]map[string]float64 {
	if result == nil {
		return nil
	}

	frameInterval := result.FrameInterval
	if frameInterval <= 0 {
		frameInterval = 2
	}

	out := map[string]map[string]float64{}
	for category, byTag := range result.Spans {
		totals := map[string]float64{}
		for tag, spans := range byTag {
			var total float64
			for _, span := range spans {
				total += span.Duration(frameInterval)
			}
			totals[tag] = total
		}
		out[category] = totals
	}
	return out
}
