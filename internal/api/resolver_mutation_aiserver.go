package api

import (
	"context"
	"errors"

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
