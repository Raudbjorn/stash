package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/tagging"
	"github.com/stashapp/stash/internal/aiserver/task"
	"github.com/stashapp/stash/pkg/aitag"
)

// AI tagging endpoints.
//
// Analysis itself goes through the scheduler as an action rather than being
// served synchronously here: a scene takes minutes, and an HTTP request that
// holds a connection open for that long is a request that times out. These
// endpoints report status, regenerate markers from stored results, and drive
// training - all of which are fast.

// taggingBackend is the slice of the server this file needs.
type taggingBackend interface {
	// Tagging runs analyses. Nil when the subsystem is not running.
	Tagging() *tagging.Service
	// Trainer fits heads. Nil when the subsystem is not running.
	Trainer() *tagging.Trainer
	// TaggingStatus reports whether analysis can run, and why not.
	TaggingStatus() tagging.Status
}

func (s *Server) taggingOrError(w http.ResponseWriter) (taggingBackend, bool) {
	backend, ok := s.backend.(taggingBackend)
	if !ok || backend.Tagging() == nil {
		writeError(w, http.StatusServiceUnavailable, "AI tagging is unavailable")
		return nil, false
	}
	return backend, true
}

// GET /tagging/status
//
// The endpoint the settings page renders: it explains why analysis cannot run,
// which is otherwise something the user has to read the log to discover.
func (s *Server) handleTaggingStatus(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.backend.(taggingBackend)
	if !ok {
		writeJSON(w, http.StatusOK, tagging.Status{
			Message: "This build has no AI tagging support.",
		})
		return
	}
	writeJSON(w, http.StatusOK, backend.TaggingStatus())
}

type vlmEvalHTTPReq struct {
	SceneIDs      []int    `json:"scene_ids"`
	FrameInterval *float64 `json:"frame_interval,omitempty"`
}

type vlmEvalSubmitResponse struct {
	TaskID string `json:"task_id"`
	Status string `json:"status"`
}

func writeVLMProviderRequired(w http.ResponseWriter, message string) {
	writeError(w, http.StatusServiceUnavailable, map[string]any{
		"code": "VLM_PROVIDER_REQUIRED", "message": message,
	})
}

// POST /tagging/vlm/eval queues read-only marker-grounded evaluation.
func (s *Server) handleVLMEval(w http.ResponseWriter, r *http.Request) {
	if !s.backend.Ready() {
		writeVLMProviderRequired(w, "AI tagging is disabled or unavailable")
		return
	}
	backend, ok := s.backend.(taggingBackend)
	if !ok || backend.Tagging() == nil {
		writeVLMProviderRequired(w, "AI tagging is disabled or unavailable")
		return
	}
	provider := backend.Tagging().Provider()
	if provider == nil || provider.Name() != tagging.ProviderVLM {
		writeVLMProviderRequired(w, "Select the llama_vlm provider before evaluation")
		return
	}
	if _, ok := provider.(aitag.FrameClassifier); !ok {
		writeVLMProviderRequired(w, "The active provider cannot classify evaluation frames")
		return
	}

	var body vlmEvalHTTPReq
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.FrameInterval != nil && *body.FrameInterval <= 0 {
		writeError(w, http.StatusBadRequest, "frame_interval must be positive")
		return
	}
	request := tagging.VLMEvalRequest{SceneIDs: body.SceneIDs}
	if body.FrameInterval != nil {
		request.FrameInterval = *body.FrameInterval
	}
	normalized, err := tagging.NormalizeVLMEvalRequest(request)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ids := make([]string, len(normalized.SceneIDs))
	for i, id := range normalized.SceneIDs {
		ids[i] = strconv.Itoa(id)
	}
	in := action.ContextInput{Page: "scenes"}
	if len(ids) == 1 {
		in.EntityID = &ids[0]
		in.IsDetailView = true
	} else {
		in.SelectedIDs = ids
	}
	registration, ok := s.backend.Actions().Resolve(tagging.EvalActionID, in)
	if !ok {
		writeVLMProviderRequired(w, "VLM evaluation is not registered")
		return
	}
	params := map[string]any{"frame_interval": normalized.FrameInterval}
	tasks := s.backend.Tasks()
	if tasks == nil {
		writeVLMProviderRequired(w, "The AI task scheduler is unavailable")
		return
	}
	if registration.Definition.DeduplicateSubmissions {
		if duplicate, found := tasks.FindDuplicate(registration.Definition, in, params); found {
			writeError(w, http.StatusConflict, map[string]any{
				"code": "ACTION_ALREADY_IN_PROGRESS", "task_id": duplicate.ID,
				"status":  string(duplicate.Status),
				"message": "Action '" + registration.Definition.Label + "' is already processing for this selection.",
			})
			return
		}
	}
	record, err := tasks.Submit(
		registration.Definition, registration.Handler, in, params, task.PriorityLow, task.SubmitOptions{})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, vlmEvalSubmitResponse{TaskID: record.ID, Status: string(record.Status)})
}

// regenerateRequest is the body of POST /tagging/regenerate.
type regenerateRequest struct {
	SceneID int    `json:"scene_id"`
	Service string `json:"service"`

	CreateMissingTags bool `json:"create_missing_tags"`
	DryRun            bool `json:"dry_run"`
	// KeepPrevious leaves earlier generated markers in place. Off by default:
	// without replacement a re-tune leaves two overlapping sets.
	KeepPrevious bool `json:"keep_previous"`
}

// POST /tagging/regenerate
//
// Rebuilds markers from ALREADY STORED spans. This is what makes the raw spans
// worth storing separately from the markers: re-tuning the clustering across a
// library costs a database read per scene rather than hours of inference.
func (s *Server) handleTaggingRegenerate(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.taggingOrError(w)
	if !ok {
		return
	}

	var body regenerateRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.SceneID <= 0 {
		writeError(w, http.StatusBadRequest, "scene_id is required")
		return
	}
	if body.Service == "" {
		writeError(w, http.StatusBadRequest, "service is required")
		return
	}

	opts := tagging.DefaultWritebackOptions()
	opts.CreateMissingTags = body.CreateMissingTags
	opts.DryRun = body.DryRun
	opts.ReplacePrevious = !body.KeepPrevious

	result, err := backend.Tagging().RegenerateMarkers(r.Context(), body.Service, body.SceneID, opts)
	if err != nil {
		writeTaggingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /tagging/spans/{scene_id}
//
// The stored raw spans for a scene, which is what a UI needs to show what was
// detected before any marker rules were applied.
func (s *Server) handleTaggingSpans(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "the AI database is not available")
		return
	}

	sceneID, err := strconv.Atoi(chi.URLParam(r, "scene_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "scene_id must be a number")
		return
	}

	service := r.URL.Query().Get("service")
	if service == "" {
		writeError(w, http.StatusBadRequest, "a service query parameter is required")
		return
	}

	// Keyed by the provider's LABEL rather than by resolved tag id: an analysis
	// whose labels have no matching Stash tags would otherwise appear to have
	// found nothing, which is the opposite of what the user needs to see.
	// Zero: every run, since this view is "everything ever detected for this
	// scene". Regeneration deliberately uses only the latest.
	spans, err := db.GetSceneSpansByLabel(r.Context(), service, sceneID, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, spans)
}

// trainRequest is the body of POST /tagging/train.
type trainRequest struct {
	Service string   `json:"service"`
	Model   string   `json:"model"`
	Name    string   `json:"name"`
	Tags    []string `json:"tags"`
	// IncludeGenerated trains on markers this server created. Off by default,
	// and deliberately so: training on your own output fits the head to its
	// predecessor's mistakes rather than to the user's judgement.
	IncludeGenerated bool `json:"include_generated"`

	Epochs       int     `json:"epochs"`
	LearningRate float32 `json:"learning_rate"`
	MinPositives int     `json:"min_positives"`
}

// POST /tagging/train
//
// Fits a head from the user's own markers. Synchronous because it is fast: the
// expensive part - the embeddings - was done during analysis, and the fit
// itself is seconds.
func (s *Server) handleTaggingTrain(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.taggingOrError(w)
	if !ok {
		return
	}
	trainer := backend.Trainer()
	if trainer == nil {
		writeError(w, http.StatusServiceUnavailable, "training is unavailable")
		return
	}

	var body trainRequest
	if !decodeJSON(w, r, &body) {
		return
	}
	if body.Service == "" || body.Model == "" {
		writeError(w, http.StatusBadRequest, "service and model are required")
		return
	}

	req := tagging.DefaultTrainRequest(body.Service, body.Model)
	if body.Name != "" {
		req.Name = body.Name
	}
	req.IncludeTags = body.Tags
	req.ExcludeGenerated = !body.IncludeGenerated

	if body.Epochs > 0 {
		req.Options.Epochs = body.Epochs
	}
	if body.LearningRate > 0 {
		req.Options.LearningRate = body.LearningRate
	}
	if body.MinPositives > 0 {
		req.Options.MinPositives = body.MinPositives
	}

	result, err := trainer.Train(r.Context(), req)
	if err != nil {
		writeTaggingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// GET /tagging/heads
func (s *Server) handleTaggingHeads(w http.ResponseWriter, r *http.Request) {
	db := s.backend.DB()
	if db == nil {
		writeError(w, http.StatusServiceUnavailable, "the AI database is not available")
		return
	}

	heads, err := db.ListTrainedHeads(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Weights are deliberately absent: a list for the UI does not need
	// megabytes of them, and ListTrainedHeads does not load them.
	type headModel struct {
		Name        string         `json:"name"`
		EmbedModel  string         `json:"embed_model"`
		Dim         int            `json:"dim"`
		Labels      []string       `json:"labels"`
		Metrics     map[string]any `json:"metrics,omitempty"`
		SampleCount int            `json:"sample_count"`
	}

	out := make([]headModel, 0, len(heads))
	for _, head := range heads {
		out = append(out, headModel{
			Name:        head.Name,
			EmbedModel:  head.EmbedModel,
			Dim:         head.Dim,
			Labels:      head.Labels,
			Metrics:     head.Metrics,
			SampleCount: head.SampleCount,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// writeTaggingError maps a tagging error to a status the UI can act on.
func writeTaggingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tagging.ErrNoProvider):
		writeError(w, http.StatusServiceUnavailable, map[string]any{
			"code":    "NO_PROVIDER",
			"message": err.Error(),
		})
	case errors.Is(err, tagging.ErrNothingToRegenerate):
		writeError(w, http.StatusUnprocessableEntity, map[string]any{
			"code":    "NO_STORED_SPANS",
			"message": err.Error(),
		})
	case errors.Is(err, tagging.ErrNoTrainingData):
		// A fresh installation with nothing analysed yet is a normal state, not
		// a server failure.
		writeError(w, http.StatusUnprocessableEntity, map[string]any{
			"code":    "NO_TRAINING_DATA",
			"message": err.Error(),
		})
	case errors.Is(err, tagging.ErrNoFile):
		writeError(w, http.StatusNotFound, map[string]any{
			"code":    "NO_FILE",
			"message": err.Error(),
		})
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
