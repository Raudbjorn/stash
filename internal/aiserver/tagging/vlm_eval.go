package tagging

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/aitag/native"
)

const (
	// EvalActionID is the scheduler action for marker-grounded VLM evaluation.
	EvalActionID  = "ai.tagging.vlm_eval"
	evalFrameSize = 512
)

var (
	// ErrVLMProviderRequired reports that evaluation has no active frame classifier.
	ErrVLMProviderRequired = errors.New("llama_vlm provider is required for evaluation")
	// ErrNoEvaluationData reports that no configured label has sampled positives.
	ErrNoEvaluationData = errors.New("no marker-grounded evaluation data is available")
)

// VLMEvalRequest selects scenes and a sparse sampling interval.
type VLMEvalRequest struct {
	SceneIDs      []int   `json:"scene_ids"`
	FrameInterval float64 `json:"frame_interval,omitempty"`
}

// VLMEvalLabel contains one configured label's confusion-matrix metrics.
type VLMEvalLabel struct {
	Samples              int      `json:"samples"`
	GroundTruthPositives int      `json:"ground_truth_positives"`
	PredictedPositives   int      `json:"predicted_positives"`
	TruePositives        int      `json:"true_positives"`
	Precision            *float64 `json:"precision"`
	Recall               *float64 `json:"recall"`
	F1                   *float64 `json:"f1"`
	Measured             bool     `json:"measured"`
	Reason               string   `json:"reason,omitempty"`
}

// VLMEvalResult is a read-only quality report against human markers.
type VLMEvalResult struct {
	Provider      string                  `json:"provider"`
	Model         string                  `json:"model"`
	SceneIDs      []int                   `json:"scene_ids"`
	FrameInterval float64                 `json:"frame_interval"`
	Frames        int                     `json:"frames"`
	MacroF1       *float64                `json:"macro_f1"`
	PerLabel      map[string]VLMEvalLabel `json:"per_label"`
}

// VLMEvalProgress reports both scene and global frame counts.
type VLMEvalProgress struct {
	Scene    int
	Scenes   int
	Frames   int
	Fraction float64
	Message  string
}

type evalCount struct {
	samples, truth, predicted, truePositive int
}

// EvaluateVLM classifies sampled frames against human-marker intervals only.
// It never enters analysis, clustering, storage, or marker writeback paths.
func (s *Service) EvaluateVLM(
	ctx context.Context,
	req VLMEvalRequest,
	sink func(VLMEvalProgress),
	cancelled func() bool,
) (*VLMEvalResult, error) {
	normalized, err := NormalizeVLMEvalRequest(req)
	if err != nil {
		return nil, err
	}
	sceneIDs, interval := normalized.SceneIDs, normalized.FrameInterval
	provider := s.Provider()
	classifier, ok := provider.(aitag.FrameClassifier)
	if provider == nil || provider.Name() != ProviderVLM || !ok {
		return nil, ErrVLMProviderRequired
	}
	labels := classifier.Labels()
	if len(labels) == 0 {
		return nil, fmt.Errorf("%w: the provider has no configured labels", ErrNoEvaluationData)
	}
	models, err := provider.Models(ctx)
	if err != nil {
		return nil, err
	}
	model := ""
	if len(models) > 0 {
		model = models[0].Name
	}
	if s.db == nil {
		return nil, fmt.Errorf("the AI database is not available")
	}

	include := make(map[string]bool, len(labels))
	for _, label := range labels {
		include[strings.ToLower(label)] = true
	}
	counts := make(map[string]*evalCount, len(labels))
	for _, label := range labels {
		counts[label] = &evalCount{}
	}
	framesProcessed := 0
	for sceneIndex, sceneID := range sceneIDs {
		if err := evalCancelled(ctx, cancelled); err != nil {
			return nil, err
		}
		generatedIDs, err := s.db.GeneratedMarkerIDs(ctx, sceneID)
		if err != nil {
			return nil, err
		}
		generated := make(map[int]bool, len(generatedIDs))
		for _, id := range generatedIDs {
			generated[id] = true
		}
		intervals, err := markerIntervals(ctx, s.repo, sceneID, generated, include)
		if err != nil {
			return nil, err
		}
		path, err := s.scenePath(ctx, sceneID)
		if err != nil {
			return nil, err
		}
		duration, probeErr := native.ProbeDuration(ctx, s.ffmpegPath, path)
		if probeErr != nil {
			duration = 0
		}
		frames, err := native.OpenFrames(ctx, s.ffmpegPath, path, native.ExtractOptions{
			Interval: interval,
			Size:     evalFrameSize,
		})
		if err != nil {
			return nil, err
		}
		var buffer []byte
		for {
			if err := evalCancelled(ctx, cancelled); err != nil {
				frames.Close()
				return nil, err
			}
			frame, err := frames.Next(buffer)
			if err != nil {
				frames.Close()
				return nil, err
			}
			if frame == nil {
				break
			}
			buffer = frame.RGB
			predictions, err := classifier.ClassifyFrame(ctx, frame.RGB, frames.Width, frames.Height)
			if err != nil {
				frames.Close()
				return nil, err
			}
			if err := evalCancelled(ctx, cancelled); err != nil {
				frames.Close()
				return nil, err
			}
			if len(predictions) != len(labels) {
				frames.Close()
				return nil, fmt.Errorf("frame classifier returned %d labels, want %d", len(predictions), len(labels))
			}
			truth := make(map[string]bool)
			for _, marker := range intervals {
				if frame.Time >= marker.start && frame.Time < marker.end {
					truth[strings.ToLower(strings.TrimSpace(marker.tag))] = true
				}
			}
			for _, label := range labels {
				predicted, exists := predictions[label]
				if !exists {
					frames.Close()
					return nil, fmt.Errorf("frame classifier omitted label %q", label)
				}
				count := counts[label]
				count.samples++
				positive := truth[strings.ToLower(label)]
				if positive {
					count.truth++
				}
				if predicted {
					count.predicted++
				}
				if positive && predicted {
					count.truePositive++
				}
			}
			framesProcessed++
			if sink != nil {
				fraction := float64(sceneIndex) / float64(len(sceneIDs))
				if duration > 0 {
					fraction += min((frame.Time+interval)/duration, 1) / float64(len(sceneIDs))
				}
				sink(VLMEvalProgress{
					Scene: sceneIndex + 1, Scenes: len(sceneIDs), Frames: framesProcessed,
					Fraction: fraction,
					Message:  fmt.Sprintf("Evaluating scene %d of %d, frame %d", sceneIndex+1, len(sceneIDs), frames.Count()),
				})
			}
		}
		if err := frames.Finish(); err != nil {
			return nil, err
		}
	}

	perLabel, macroF1, measured := finishEvalMetrics(labels, counts)
	if measured == 0 {
		return nil, fmt.Errorf("%w: no configured label has a sampled human-positive frame", ErrNoEvaluationData)
	}
	if sink != nil {
		sink(VLMEvalProgress{
			Scene: len(sceneIDs), Scenes: len(sceneIDs), Frames: framesProcessed,
			Fraction: 1, Message: fmt.Sprintf("Evaluated %d scenes and %d frames", len(sceneIDs), framesProcessed),
		})
	}
	return &VLMEvalResult{
		Provider: provider.Name(), Model: model, SceneIDs: sceneIDs,
		FrameInterval: interval, Frames: framesProcessed,
		MacroF1: macroF1, PerLabel: perLabel,
	}, nil
}

// NormalizeVLMEvalRequest validates and canonicalizes task identity.
func NormalizeVLMEvalRequest(req VLMEvalRequest) (VLMEvalRequest, error) {
	if len(req.SceneIDs) == 0 {
		return VLMEvalRequest{}, fmt.Errorf("at least one scene_id is required")
	}
	sceneIDs := append([]int(nil), req.SceneIDs...)
	for _, id := range sceneIDs {
		if id <= 0 {
			return VLMEvalRequest{}, fmt.Errorf("scene_id must be positive, got %d", id)
		}
	}
	sort.Ints(sceneIDs)
	canonical := sceneIDs[:0]
	for _, id := range sceneIDs {
		if len(canonical) == 0 || canonical[len(canonical)-1] != id {
			canonical = append(canonical, id)
		}
	}
	interval := req.FrameInterval
	if interval == 0 {
		interval = llamaprov.DefaultFrameInterval
	}
	if interval <= 0 || math.IsNaN(interval) || math.IsInf(interval, 0) {
		return VLMEvalRequest{}, fmt.Errorf("frame_interval must be positive")
	}
	return VLMEvalRequest{SceneIDs: canonical, FrameInterval: interval}, nil
}

func evalCancelled(ctx context.Context, cancelled func() bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cancelled != nil && cancelled() {
		return context.Canceled
	}
	return nil
}

func finishEvalMetrics(labels []string, counts map[string]*evalCount) (map[string]VLMEvalLabel, *float64, int) {
	out := make(map[string]VLMEvalLabel, len(labels))
	macro := 0.0
	measured := 0
	for _, label := range labels {
		count := counts[label]
		metric := VLMEvalLabel{
			Samples: count.samples, GroundTruthPositives: count.truth,
			PredictedPositives: count.predicted, TruePositives: count.truePositive,
		}
		if count.truth == 0 {
			metric.Reason = "no sampled human-positive frames"
			out[label] = metric
			continue
		}
		metric.Measured = true
		measured++
		recall := float64(count.truePositive) / float64(count.truth)
		metric.Recall = &recall
		if count.predicted == 0 {
			zero := 0.0
			metric.F1 = &zero
			out[label] = metric
			continue
		}
		precision := float64(count.truePositive) / float64(count.predicted)
		metric.Precision = &precision
		f1 := 0.0
		if precision+recall > 0 {
			f1 = 2 * precision * recall / (precision + recall)
		}
		metric.F1 = &f1
		macro += f1
		out[label] = metric
	}
	if measured == 0 {
		return out, nil, 0
	}
	value := macro / float64(measured)
	return out, &value, measured
}

func (s *Service) registerVLMEvaluation(actions *action.Registry) {
	actions.Register(action.Definition{
		ID:          EvalActionID,
		Label:       "Evaluate vision-language tagging",
		Description: "Measure the active llama_vlm labels against human scene markers.",
		Service:     ServiceName,
		ResultKind:  action.ResultNotification,
		Contexts: []action.ContextRule{
			{Pages: []string{"scenes"}, Selection: action.SelectionSingle},
			{Pages: []string{"scenes"}, Selection: action.SelectionMulti},
		},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"frame_interval": map[string]any{
					"type": "number", "title": "Frame interval (seconds)",
					"default": llamaprov.DefaultFrameInterval,
				},
			},
		},
		DeduplicateSubmissions: true,
	}, s.vlmEvalHandler())
}

func (s *Service) vlmEvalHandler() action.Handler {
	return func(ctx context.Context, in action.ContextInput, params map[string]any, task action.Handle) (any, error) {
		sceneIDs, err := scenesFromContext(in)
		if err != nil {
			return nil, err
		}
		interval := 0.0
		if raw, exists := params["frame_interval"]; exists {
			value, ok := raw.(float64)
			if !ok || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("frame_interval must be positive")
			}
			interval = value
		}
		return s.EvaluateVLM(ctx, VLMEvalRequest{SceneIDs: sceneIDs, FrameInterval: interval}, func(progress VLMEvalProgress) {
			task.Progress(map[string]any{
				"progress": progress.Fraction, "message": progress.Message,
				"scene": progress.Scene, "scenes": progress.Scenes, "frames": progress.Frames,
			})
		}, task.Cancelled)
	}
}
