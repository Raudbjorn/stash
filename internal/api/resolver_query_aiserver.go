package api

import (
	"context"
	"strconv"

	"github.com/stashapp/stash/internal/aiserver"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
)

func aiServerStateFromHealth(s aiserver.State) AIServerState {
	switch s {
	case aiserver.StateReady:
		return AIServerStateReady
	case aiserver.StateFailed:
		return AIServerStateFailed
	case aiserver.StateStopped:
		return AIServerStateStopped
	default:
		return AIServerStateDisabled
	}
}

// buildAIServerConfig reads the current AI server configuration off Stash's
// config system. Never echoes the effective OpenAI key (which may fall back
// to $OPENAI_API_KEY) - only whether one is stored.
func buildAIServerConfig(c *manager.Manager) *AIServerConfig {
	cfg := c.Config

	var provider *AITaggingProvider
	if p := aiTaggingProviderFromString(cfg.GetAITaggingProvider()); p != nil {
		provider = p
	}

	return &AIServerConfig{
		Enabled:              cfg.GetAIEnabled(),
		TaggingProvider:      provider,
		TaggingServerURL:     cfg.GetAITaggingServerURL(),
		TaggingOpenAIKeySet:  cfg.GetAITaggingOpenAIKeyConfigured(),
		TaggingModelDir:      cfg.GetAITaggingModelDir(),
		TaggingRulesDir:      cfg.GetAITaggingRulesDir(),
		TaggingFrameInterval: cfg.GetAITaggingFrameInterval(),
		TaggingThreshold:     cfg.GetAITaggingThreshold(),
		TaggingMaxSpanMerge:  cfg.GetAITaggingMaxSpanMerge(),
		TaggingVLMModel:      cfg.GetAITaggingVLMModel(),
		TaggingVLMLabels:     cfg.GetAITaggingVLMLabels(),
		TaggingVLMGPULayers:  cfg.GetAITaggingVLMGPULayers(),
		TaggingVLMContext:    cfg.GetAITaggingVLMContext(),
	}
}

func aiTaggingProviderFromString(s string) *AITaggingProvider {
	var p AITaggingProvider
	switch s {
	case "native":
		p = AITaggingProviderNative
	case "skier_aitagging":
		p = AITaggingProviderSkierAitagging
	case "openai_moderation":
		p = AITaggingProviderOpenaiModeration
	case "llama_vlm":
		p = AITaggingProviderLlamaVlm
	default:
		return nil
	}
	return &p
}

func aiTaggingProviderToString(p AITaggingProvider) string {
	switch p {
	case AITaggingProviderNative:
		return "native"
	case AITaggingProviderSkierAitagging:
		return "skier_aitagging"
	case AITaggingProviderOpenaiModeration:
		return "openai_moderation"
	case AITaggingProviderLlamaVlm:
		return "llama_vlm"
	default:
		return ""
	}
}

func (r *queryResolver) AiServerStatus(ctx context.Context) (*AIServerStatus, error) {
	mgr := manager.GetInstance()
	srv := mgr.AIServer

	health := srv.Health(ctx)
	taggingStatus := srv.TaggingStatus()

	var schemaVersion, errMsg *string
	if health.SchemaVersion != "" {
		schemaVersion = &health.SchemaVersion
	}
	if health.Error != "" {
		errMsg = &health.Error
	}

	return &AIServerStatus{
		State:          aiServerStateFromHealth(health.State),
		Enabled:        srv.Enabled(),
		Ready:          srv.Ready(),
		HasVLMProvider: taggingStatus.Provider == llamaprov.ProviderName && taggingStatus.Available,
		BackendVersion: health.BackendVersion,
		SchemaVersion:  schemaVersion,
		Error:          errMsg,
		Database: &AIServerHealthComponent{
			Status:  string(health.Database.Status),
			Message: health.Database.Message,
		},
		Config: buildAIServerConfig(mgr),
	}, nil
}

func (r *queryResolver) AiTaggingStatus(ctx context.Context) (*AITaggingStatus, error) {
	mgr := manager.GetInstance()
	status := mgr.AIServer.TaggingStatus()

	var message, remediation *string
	if status.Message != "" {
		message = &status.Message
	}
	if status.Remediation != "" {
		remediation = &status.Remediation
	}

	return &AITaggingStatus{
		Provider:    status.Provider,
		Available:   status.Available,
		Message:     message,
		Remediation: remediation,
	}, nil
}

func (r *queryResolver) AiTaggingSpans(ctx context.Context, sceneID string) ([]*AITaggingSpanGroup, error) {
	mgr := manager.GetInstance()
	db := mgr.AIServer.DB()
	if db == nil {
		return []*AITaggingSpanGroup{}, nil
	}

	id, err := strconv.Atoi(sceneID)
	if err != nil {
		return nil, err
	}

	service := mgr.Config.GetAITaggingProvider()
	if service == "" {
		return []*AITaggingSpanGroup{}, nil
	}

	byCategory, err := db.GetSceneSpansByLabel(ctx, service, id, 0)
	if err != nil {
		return nil, err
	}

	ret := make([]*AITaggingSpanGroup, 0, len(byCategory))
	for category, byLabel := range byCategory {
		for label, spans := range byLabel {
			group := &AITaggingSpanGroup{
				Category: category,
				Label:    label,
				Spans:    make([]*AITaggingSpan, 0, len(spans)),
			}
			for _, s := range spans {
				var tagID *string
				if s.TagID != nil {
					id := strconv.Itoa(*s.TagID)
					tagID = &id
				}
				group.Spans = append(group.Spans, &AITaggingSpan{
					Start:      s.Start,
					End:        s.End,
					Confidence: s.Confidence,
					TagID:      tagID,
				})
			}
			ret = append(ret, group)
		}
	}
	return ret, nil
}
