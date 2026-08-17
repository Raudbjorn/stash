package api

import (
	"context"
	"errors"
	"net/url"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/tagging"
	"github.com/stashapp/stash/internal/aiserver/task"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/internal/manager/config"
	"github.com/stashapp/stash/pkg/logger"
)

// ConfigureAIServer persists the AI server's enabled flag and tagging
// settings, then restarts the subsystem so the change takes effect without a
// restart of Stash.
//
// The restart runs on a detached goroutine: Refresh drains any in-flight AI
// task before starting again, which can take a while, and this mutation must
// not block on it. aiserver.Server.Refresh serializes concurrent refreshes
// against each other, so an overlapping configureAIServer call queues behind
// the one in progress rather than interleaving with it; config writes
// themselves are unserialized, so the config actually applied is still
// whichever mutation's c.Write() ran last.
func (r *mutationResolver) ConfigureAIServer(ctx context.Context, input AIServerConfigInput) (*AIServerConfig, error) {
	c := config.GetInstance()
	switch input.TaggingVLMAcceptMode {
	case "shadow", "rescue", "strict":
	default:
		return nil, errors.New("taggingVLMAcceptMode must be shadow, rescue, or strict")
	}
	if input.TaggingVLMVoyageRerankTopK <= 0 || input.TaggingVLMVoyageRerankTopK > 1000 {
		return nil, errors.New("taggingVLMVoyageRerankTopK must be between 1 and 1000")
	}
	voyageEndpoint, err := url.ParseRequestURI(input.TaggingVLMVoyageEndpoint)
	if err != nil || voyageEndpoint.Host == "" || (voyageEndpoint.Scheme != "http" && voyageEndpoint.Scheme != "https") {
		return nil, errors.New("taggingVLMVoyageEndpoint must be an absolute HTTP(S) URL")
	}
	if input.TaggingVLMVoyageSegmentSecs <= 0 {
		return nil, errors.New("taggingVLMVoyageSegmentSecs must be positive")
	}
	if input.TaggingVLMVoyageDimension <= 0 {
		return nil, errors.New("taggingVLMVoyageDimension must be positive")
	}
	embeddingEndpoint, err := url.ParseRequestURI(input.TaggingVLMVoyageEmbeddingEndpoint)
	if err != nil || embeddingEndpoint.Host == "" || (embeddingEndpoint.Scheme != "http" && embeddingEndpoint.Scheme != "https") {
		return nil, errors.New("taggingVLMVoyageEmbeddingEndpoint must be an absolute HTTP(S) URL")
	}

	c.SetBool(config.AIEnabled, input.Enabled)
	if input.TaggingProvider != nil {
		c.SetString(config.AITaggingProvider, aiTaggingProviderToString(*input.TaggingProvider))
	} else {
		c.SetString(config.AITaggingProvider, "")
	}
	c.SetString(config.AITaggingServerURL, input.TaggingServerURL)
	if input.TaggingOpenAIKey != nil {
		c.SetString(config.AITaggingOpenAIKey, *input.TaggingOpenAIKey)
	}
	c.SetString(config.AITaggingModelDir, input.TaggingModelDir)
	c.SetString(config.AITaggingRulesDir, input.TaggingRulesDir)
	c.SetFloat(config.AITaggingFrameInterval, input.TaggingFrameInterval)
	c.SetFloat(config.AITaggingThreshold, input.TaggingThreshold)
	c.SetFloat(config.AITaggingMaxSpanMerge, input.TaggingMaxSpanMerge)
	c.SetString(config.AITaggingVLMModel, input.TaggingVLMModel)
	c.SetInterface(config.AITaggingVLMLabels, input.TaggingVLMLabels)
	c.SetInt(config.AITaggingVLMGPULayers, input.TaggingVLMGPULayers)
	c.SetInt(config.AITaggingVLMContext, input.TaggingVLMContext)
	c.SetString(config.AITaggingAnalyzeMode, input.TaggingAnalyzeMode)
	c.SetString(config.AITaggingVLMAcceptMode, input.TaggingVLMAcceptMode)
	if input.TaggingVLMVoyageAPIKey != nil {
		c.SetString(config.AITaggingVLMVoyageAPIKey, *input.TaggingVLMVoyageAPIKey)
	}
	c.SetString(config.AITaggingVLMVoyageRerankModel, input.TaggingVLMVoyageRerankModel)
	c.SetInt(config.AITaggingVLMVoyageRerankTopK, input.TaggingVLMVoyageRerankTopK)
	c.SetString(config.AITaggingVLMVoyageEndpoint, input.TaggingVLMVoyageEndpoint)
	c.SetBool(config.AITaggingVLMVoyageVideoEnabled, input.TaggingVLMVoyageVideoEnabled)
	c.SetString(config.AITaggingVLMVoyageVideoModel, input.TaggingVLMVoyageVideoModel)
	c.SetFloat(config.AITaggingVLMVoyageSegmentSecs, input.TaggingVLMVoyageSegmentSecs)
	c.SetInt(config.AITaggingVLMVoyageDimension, input.TaggingVLMVoyageDimension)
	c.SetString(config.AITaggingVLMVoyageEmbeddingEndpoint, input.TaggingVLMVoyageEmbeddingEndpoint)
	c.SetString(config.AITaggingTaxonomyEndpoint, input.TaggingTaxonomyEndpoint)
	if input.TaggingTaxonomyAPIKey != nil {
		c.SetString(config.AITaggingTaxonomyAPIKey, *input.TaggingTaxonomyAPIKey)
	}
	c.SetInterface(config.AITaggingTaxonomyCategories, input.TaggingTaxonomyCategories)
	c.SetInt(config.AITaggingTaxonomyMaxCandidates, input.TaggingTaxonomyMaxCandidates)

	if err := c.Write(); err != nil {
		return nil, err
	}

	mgr := manager.GetInstance()
	go func() {
		mgr.RefreshAIServer(context.Background())
		logger.Infof("AI server refreshed after configuration change (enabled=%v)", input.Enabled)
	}()

	return buildAIServerConfig(mgr), nil
}

func (r *mutationResolver) RefreshAITaggingTaxonomy(ctx context.Context) (*AIServerTaxonomyRefresh, error) {
	mgr := manager.GetInstance()
	service := mgr.AIServer.Tagging()
	if service == nil {
		return nil, errors.New("AI tagging service is not running")
	}
	status, err := service.RefreshTaxonomy(
		ctx,
		mgr.Config.GetAITaggingTaxonomyEndpoint(),
		mgr.Config.GetAITaggingTaxonomyAPIKey(),
	)
	if err != nil {
		return nil, err
	}
	return &AIServerTaxonomyRefresh{
		Endpoint:    status.Endpoint,
		Entries:     status.Entries,
		RefreshedAt: status.RefreshedAt,
	}, nil
}

func (r *mutationResolver) AnalyzeSceneWithAi(ctx context.Context, sceneID string) (*AnalyzeSceneWithAIResult, error) {
	mgr := manager.GetInstance()

	actx := action.ContextInput{
		Page:         "scenes",
		EntityID:     &sceneID,
		IsDetailView: true,
		SelectedIDs:  []string{sceneID},
	}

	rec, err := mgr.AIServer.SubmitAction(ctx, tagging.ActionID, actx, nil, nil)
	if err != nil {
		var dup *task.ErrDuplicateSubmission
		if errors.As(err, &dup) {
			return &AnalyzeSceneWithAIResult{
				TaskID:            dup.Dup.ID,
				Status:            string(dup.Dup.Status),
				AlreadyInProgress: true,
			}, nil
		}
		return nil, err
	}

	return &AnalyzeSceneWithAIResult{
		TaskID:            rec.ID,
		Status:            string(rec.Status),
		AlreadyInProgress: false,
	}, nil
}

func (r *mutationResolver) CancelAITask(ctx context.Context, id string) (bool, error) {
	mgr := manager.GetInstance()
	tasks := mgr.AIServer.Tasks()
	if tasks == nil {
		return false, nil
	}
	return tasks.Cancel(id), nil
}
