package tagging

import (
	"context"
	"fmt"
	"strconv"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/logger"
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

const (
	analysisProfileLocal       = "local"
	analysisProfileLocalVoyage = "local_voyage"
	analysisProfileVoyage      = "voyage"
)

// Register adds the analysis action and its service to the registries.
func (s *Service) Register(actions *action.Registry, services *service.Registry) {
	services.Register(&analysisService{})
	properties := map[string]any{
		"frame_interval": map[string]any{
			"type": "number", "title": "Frame interval (seconds)", "default": s.defaultInterval,
		},
		"threshold": map[string]any{
			"type": "number", "title": "Confidence or similarity threshold", "default": 0.5,
		},
		"vr": map[string]any{
			"type": "boolean", "title": "VR video", "default": false,
		},
		"create_missing_tags": map[string]any{
			"type": "boolean", "title": "Create missing tags", "default": true,
		},
		"apply_scene_tags": map[string]any{
			"type": "boolean", "title": "Apply detected tags to scenes", "default": true,
		},
		"store_embeddings": map[string]any{
			"type":    "boolean",
			"title":   "Cache embeddings for training",
			"default": true,
		},
	}
	if values, labels, defaultValue := s.analysisProfileOptions(); len(values) > 0 {
		properties["analysis_service"] = map[string]any{
			"type": "string", "title": "Analysis service", "enum": values,
			"enumNames": labels, "default": defaultValue,
		}
	}
	actions.Register(action.Definition{
		ID:          ActionID,
		Label:       "Analyse with AI",
		Description: "Detect actions in this scene, apply tags, and generate markers.",
		Service:     ServiceName,
		ResultKind:  action.ResultNotification,
		Contexts: []action.ContextRule{
			{Pages: []string{"scenes"}, Selection: action.SelectionSingle},
			{Pages: []string{"scenes"}, Selection: action.SelectionMulti},
		},
		InputSchema:            map[string]any{"type": "object", "properties": properties},
		DeduplicateSubmissions: true,
	}, s.handler())
	s.registerVLMEvaluation(actions)
}

func (s *Service) analysisProfileOptions() (values, labels []string, defaultValue string) {
	provider := s.Provider()
	if provider != nil {
		if provider.Name() == "llama_vlm" {
			values = append(values, analysisProfileLocal)
			labels = append(labels, "Local VLM")
			defaultValue = analysisProfileLocal
			if s.SupportsVoyageReranking() {
				values = append(values, analysisProfileLocalVoyage)
				labels = append(labels, "Local VLM + Voyage AI reranking")
				defaultValue = analysisProfileLocalVoyage
			}
		} else {
			values = append(values, provider.Name())
			labels = append(labels, provider.Name())
			defaultValue = provider.Name()
		}
	}
	if s.SupportsVoyageTagging() {
		values = append(values, analysisProfileVoyage)
		labels = append(labels, "Voyage AI")
		if defaultValue == "" {
			defaultValue = analysisProfileVoyage
		}
	}
	return values, labels, defaultValue
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

		analysisProfile := stringParam(params, "analysis_service", "")
		var useVoyage *bool
		voyageOnly := false
		switch analysisProfile {
		case analysisProfileLocal:
			if s.Provider() == nil {
				return nil, fmt.Errorf("local analysis is not configured")
			}
			value := false
			useVoyage = &value
		case analysisProfileLocalVoyage:
			if !s.SupportsVoyageReranking() {
				return nil, fmt.Errorf("Voyage reranking is not configured")
			}
			value := true
			useVoyage = &value
		case analysisProfileVoyage:
			if !s.SupportsVoyageTagging() {
				return nil, fmt.Errorf("Voyage video tagging is not configured")
			}
			voyageOnly = true
		case "":
			if s.Provider() == nil && s.SupportsVoyageTagging() {
				analysisProfile = analysisProfileVoyage
				voyageOnly = true
			}
		default:
			provider := s.Provider()
			if provider == nil || provider.Name() != analysisProfile {
				return nil, fmt.Errorf("analysis service %q is not configured", analysisProfile)
			}
		}

		req := AnalyzeRequest{
			Options: aitag.Options{
				FrameInterval:     floatParam(params, "frame_interval", 0),
				Threshold:         floatParam(params, "threshold", 0),
				VR:                boolParam(params, "vr", false),
				WantEmbeddings:    boolParam(params, "store_embeddings", true),
				UseVoyageReranker: useVoyage,
			},
			Writeback:       DefaultWritebackOptions(),
			StoreEmbeddings: boolParam(params, "store_embeddings", true),
		}
		req.Writeback.CreateMissingTags = boolParam(params, "create_missing_tags", true)
		req.Writeback.ApplySceneTags = boolParam(params, "apply_scene_tags", true)

		var results []*AnalyzeResult
		for i, sceneID := range sceneIDs {
			if task.Cancelled() {
				return nil, context.Canceled
			}

			logger.Infof("AI analysis started scene=%d service=%s", sceneID, analysisProfileName(analysisProfile, s.Provider()))
			req.SceneID = sceneID

			// Progress is reported per scene as well as within one, so a
			// selection of fifty shows movement rather than sitting at zero
			// until the first finishes.
			base := float64(i) / float64(len(sceneIDs))
			span := 1 / float64(len(sceneIDs))

			analyze := s.AnalyzeScene
			if voyageOnly {
				analyze = s.AnalyzeSceneWithVoyage
			}
			result, err := analyze(ctx, req, func(p aitag.Progress) {
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
				logger.Errorf("AI analysis failed scene=%d service=%s: %v", sceneID, analysisProfileName(analysisProfile, s.Provider()), err)
				results = append(results, &AnalyzeResult{SceneID: sceneID, Provider: "error", Error: err.Error()})
				continue
			}
			logger.Infof("AI analysis completed scene=%d service=%s spans=%d markers=%d tags_applied=%d",
				sceneID, analysisProfileName(analysisProfile, s.Provider()), result.Spans, result.Markers, appliedTagCount(result.Write))
			results = append(results, result)
		}

		return summariseOutcome(results)
	}
}

// summarise reduces per-scene results to what a notification shows.
func summarise(results []*AnalyzeResult) map[string]any {
	var (
		spans       int
		markers     int
		created     int
		removed     int
		tagsApplied int
		tagsCreated int
		failed      int
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
			tagsApplied += result.Write.TagsApplied
			tagsCreated += result.Write.TagsCreated
		}
	}

	message := fmt.Sprintf("Analysed %d scenes: %d spans, %d markers, %d scene tags applied.",
		len(results)-failed, spans, markers, tagsApplied)
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
		"tags_applied":    tagsApplied,
		"tags_created":    tagsCreated,
		// Reported so a re-run visibly REPLACES rather than accumulates.
		// Without it the only way to tell the difference is to count markers in
		// Stash before and after, which is exactly the archaeology the
		// provenance table exists to avoid.
		"markers_removed": removed,
	}
}

func summariseOutcome(results []*AnalyzeResult) (map[string]any, error) {
	summary := summarise(results)
	failed, _ := summary["failed"].(int)
	if failed != len(results) || failed == 0 {
		return summary, nil
	}
	if failed == 1 {
		return summary, fmt.Errorf("%s", results[0].Error)
	}
	return summary, fmt.Errorf("all %d scenes failed; first error: %s", failed, results[0].Error)
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

func stringParam(params map[string]any, key, fallback string) string {
	if v, ok := params[key].(string); ok {
		return v
	}
	return fallback
}

func analysisProfileName(profile string, provider aitag.Provider) string {
	if profile != "" {
		return profile
	}
	if provider != nil {
		return provider.Name()
	}
	return "unavailable"
}

func appliedTagCount(result *WritebackResult) int {
	if result == nil {
		return 0
	}
	return result.TagsApplied
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
