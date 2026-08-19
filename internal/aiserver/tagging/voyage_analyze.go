package tagging

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/logger"
)

// AnalyzeSceneWithVoyage runs direct Voyage text-to-video taxonomy retrieval.
// It does not invoke the configured local or remote tagging provider.
func (s *Service) AnalyzeSceneWithVoyage(ctx context.Context, req AnalyzeRequest, sink aitag.Sink) (*AnalyzeResult, error) {
	s.mu.RLock()
	analyzer := s.voyageAnalyzer
	rules := s.rules
	params := s.params
	s.mu.RUnlock()
	if analyzer == nil || !analyzer.CanTag() {
		return nil, errors.New("Voyage video tagging is not configured")
	}

	started := time.Now()
	path, duration, err := s.sceneVideo(ctx, req.SceneID)
	if err != nil {
		return nil, err
	}
	if duration <= 0 {
		return nil, fmt.Errorf("scene %d has no probed video duration", req.SceneID)
	}

	result, err := analyzer.AnalyzeVideoTags(ctx, req.SceneID, path, duration, req.Options.Threshold, sink)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("Voyage video tagging returned no result")
	}
	aitag.SortSpans(result.Spans)
	supports := labelSupportsFromMetric(result.Metrics["label_supports"])
	if len(supports) > 0 {
		aggregated, aggregateErr := s.aggregateSupportParents(ctx, supports, s.TaxonomyStatus().Endpoint)
		if aggregateErr != nil {
			logger.Warnf("could not aggregate Voyage label support through tag parents for scene %d: %v", req.SceneID, aggregateErr)
		} else {
			result.Metrics["label_supports_with_parents"] = aggregated
		}
	}

	runID, err := s.storeRun(ctx, recommend.VoyageProviderName, req.SceneID, req.Options, result)
	if err != nil {
		return nil, err
	}
	out := &AnalyzeResult{
		SceneID:        req.SceneID,
		RunID:          runID,
		Provider:       recommend.VoyageProviderName,
		Duration:       duration,
		Spans:          aitag.CountSpans(result.Spans),
		Frames:         metricInt(result.Metrics["segments"]),
		Supports:       supports,
		VoyageSegments: metricInt(result.Metrics["segments"]),
	}

	if !req.SkipWriteback && (rules != nil || req.Writeback.ApplySceneTags) {
		var markers []aitag.Marker
		if rules != nil {
			markers = aitag.Cluster(result, rules, params)
		}
		out.Markers = len(markers)
		if req.Writeback.ApplySceneTags {
			req.Writeback.SceneTagNames = detectedSceneTagNames(result.Spans, markers)
		}
		write, err := s.writer.Write(ctx, recommend.VoyageProviderName, req.SceneID, runID, markers, result.FrameInterval, req.Writeback)
		if err != nil {
			return out, fmt.Errorf("write Voyage analysis results: %w", err)
		}
		out.Write = &write
	}

	out.Elapsed = time.Since(started).Seconds()
	sink.Report(aitag.Progress{
		Fraction: 1,
		Message:  fmt.Sprintf("Stored %d Voyage spans and %d markers.", out.Spans, out.Markers),
	})
	return out, nil
}
