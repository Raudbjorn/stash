package tagging

import (
	"context"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/pkg/aitag"
)

// Registering analysis as an action.
//
// Analysis runs through the scheduler rather than being an endpoint, for three
// reasons that all matter: a scene takes minutes and would time out an HTTP
// request; the scheduler already handles priority, cancellation and
// deduplication; and the existing UI's AIButton submits actions, so this
// appears there with no frontend change.

// ActionID is the analysis action's identifier.
const ActionID = "ai.tagging.analyze"

// ServiceName is the scheduler service analyses run under.
//
// Its concurrency is one: inference saturates the cores already, and two scenes
// at once finish later than two in sequence while using twice the memory.
const ServiceName = "ai_tagging"

// Register adds the analysis action and its service to the registries.
func (s *Service) Register(actions *action.Registry, services *service.Registry) {
	services.Register(&analysisService{})

	actions.Register(action.Definition{
		ID:          ActionID,
		Label:       "Analyse with AI",
		Description: "Detect actions in this scene and generate markers.",
		Service:     ServiceName,
		ResultKind:  action.ResultNotification,
		Contexts: []action.ContextRule{
			// Detail view for one scene, and library views for a selection.
			{Pages: []string{"scenes"}, Selection: action.SelectionSingle},
			{Pages: []string{"scenes"}, Selection: action.SelectionMulti},
		},
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"frame_interval": map[string]any{
					"type": "number", "title": "Frame interval (seconds)", "default": s.defaultInterval,
				},
				"threshold": map[string]any{
					"type": "number", "title": "Confidence threshold", "default": 0.5,
				},
				"vr": map[string]any{
					"type": "boolean", "title": "VR video", "default": false,
				},
				"create_missing_tags": map[string]any{
					"type": "boolean", "title": "Create missing tags", "default": false,
				},
				"store_embeddings": map[string]any{
					"type":    "boolean",
					"title":   "Cache embeddings for training",
					"default": true,
				},
			},
		},
		DeduplicateSubmissions: true,
	}, s.handler())
	s.registerVLMEvaluation(actions)
}

// handler runs one analysis, or fans out over a selection.
func (s *Service) handler() action.Handler {
	return func(ctx context.Context, in action.ContextInput, params map[string]any, task action.Handle) (any, error) {
		sceneIDs, err := scenesFromContext(in)
		if err != nil {
			return nil, err
		}
		if len(sceneIDs) == 0 {
			return nil, fmt.Errorf("no scene was selected")
		}

		req := AnalyzeRequest{
			Options: aitag.Options{
				FrameInterval:  floatParam(params, "frame_interval", 0),
				Threshold:      floatParam(params, "threshold", 0),
				VR:             boolParam(params, "vr", false),
				WantEmbeddings: boolParam(params, "store_embeddings", true),
			},
			Writeback:       DefaultWritebackOptions(),
			StoreEmbeddings: boolParam(params, "store_embeddings", true),
		}
		req.Writeback.CreateMissingTags = boolParam(params, "create_missing_tags", false)

		var results []*AnalyzeResult
		for i, sceneID := range sceneIDs {
			if task.Cancelled() {
				return nil, context.Canceled
			}

			req.SceneID = sceneID

			// Progress is reported per scene as well as within one, so a
			// selection of fifty shows movement rather than sitting at zero
			// until the first finishes.
			base := float64(i) / float64(len(sceneIDs))
			span := 1 / float64(len(sceneIDs))

			result, err := s.AnalyzeScene(ctx, req, func(p aitag.Progress) {
				fraction := base
				if p.Fraction >= 0 {
					fraction += p.Fraction * span
				}
				task.Progress(map[string]any{
					"progress": fraction,
					"message":  fmt.Sprintf("Scene %d of %d: %s", i+1, len(sceneIDs), p.Message),
					"scene_id": sceneID,
				})
			})
			if err != nil {
				// One scene failing does not abandon the rest of a selection:
				// a single unreadable file should not lose an hour of work on
				// the other forty-nine.
				results = append(results, &AnalyzeResult{SceneID: sceneID, Provider: "error", Error: err.Error()})
				continue
			}
			results = append(results, result)
		}

		return summarise(results), nil
	}
}

// summarise reduces per-scene results to what a notification shows.
func summarise(results []*AnalyzeResult) map[string]any {
	var (
		spans   int
		markers int
		created int
		removed int
		failed  int
	)
	failedScenes := make([]int, 0)
	for _, result := range results {
		if result.Error != "" || result.Provider == "error" {
			failed++
			failedScenes = append(failedScenes, result.SceneID)
			continue
		}
		spans += result.Spans
		markers += result.Markers
		if result.Write != nil {
			created += result.Write.Created
			removed += result.Write.Removed
		}
	}

	message := fmt.Sprintf("Analysed %d scenes: %d spans, %d markers.",
		len(results)-failed, spans, markers)
	if failed > 0 {
		message += fmt.Sprintf(" %d could not be analysed.", failed)
	}

	return map[string]any{
		"message":         message,
		"scenes":          len(results),
		"failed":          failed,
		"failed_scenes":   failedScenes,
		"results":         results,
		"spans":           spans,
		"markers":         markers,
		"markers_created": created,
		// Reported so a re-run visibly REPLACES rather than accumulates.
		// Without it the only way to tell the difference is to count markers in
		// Stash before and after, which is exactly the archaeology the
		// provenance table exists to avoid.
		"markers_removed": removed,
	}
}

// scenesFromContext extracts the scene ids a request applies to.
func scenesFromContext(in action.ContextInput) ([]int, error) {
	var raw []string

	if in.IsDetailView && in.EntityID != nil {
		raw = []string{*in.EntityID}
	} else if len(in.SelectedIDs) > 0 {
		raw = in.SelectedIDs
	}

	out := make([]int, 0, len(raw))
	for _, text := range raw {
		id, err := strconv.Atoi(text)
		if err != nil {
			return nil, fmt.Errorf("%q is not a scene id", text)
		}
		out = append(out, id)
	}
	return out, nil
}

func floatParam(params map[string]any, key string, fallback float64) float64 {
	if v, ok := params[key].(float64); ok {
		return v
	}
	return fallback
}

func boolParam(params map[string]any, key string, fallback bool) bool {
	if v, ok := params[key].(bool); ok {
		return v
	}
	return fallback
}

// analysisService is the scheduler's view of AI tagging.
type analysisService struct{}

func (analysisService) Name() string { return ServiceName }

// MaxConcurrency is one: the parallelism is already inside the inference
// kernels, so a second concurrent scene contends for the same cores and
// finishes both later than running them in sequence would.
func (analysisService) MaxConcurrency() int { return 1 }

// EnsureReady always returns true: this is an in-process service with no remote
// dependency, and a provider that cannot run reports that per task instead.
func (analysisService) EnsureReady(ctx context.Context) bool { return true }
