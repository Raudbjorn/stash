package api

import (
	"context"
	"strconv"

	"github.com/stashapp/stash/internal/aiserver"
	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/aitag/assets"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/ollama"
)

var (
	availableVoyageRerankModelValues = []string{
		"rerank-2.5",
		"rerank-2.5-lite",
		"rerank-2",
		"rerank-2-lite",
		"rerank-1",
		"rerank-lite-1",
	}
	availableVoyageVideoModelValues = []string{
		"voyage-multimodal-3.5",
		"voyage-multimodal-3",
	}
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
		Enabled:                           cfg.GetAIEnabled(),
		TaggingProvider:                   provider,
		TaggingServerURL:                  cfg.GetAITaggingServerURL(),
		TaggingOpenAIKeySet:               cfg.GetAITaggingOpenAIKeyConfigured(),
		TaggingModelDir:                   cfg.GetAITaggingModelDir(),
		TaggingRulesDir:                   cfg.GetAITaggingRulesDir(),
		TaggingFrameInterval:              cfg.GetAITaggingFrameInterval(),
		TaggingThreshold:                  cfg.GetAITaggingThreshold(),
		TaggingMaxSpanMerge:               cfg.GetAITaggingMaxSpanMerge(),
		TaggingVLMModel:                   cfg.GetAITaggingVLMModel(),
		TaggingVLMLabels:                  cfg.GetAITaggingVLMLabels(),
		TaggingVLMGPULayers:               cfg.GetAITaggingVLMGPULayers(),
		TaggingVLMContext:                 cfg.GetAITaggingVLMContext(),
		TaggingAnalyzeMode:                cfg.GetAITaggingAnalyzeMode(),
		TaggingVLMAcceptMode:              cfg.GetAITaggingVLMAcceptMode(),
		TaggingVLMVoyageAPIKeySet:         cfg.GetAITaggingVLMVoyageAPIKey() != "",
		TaggingVLMVoyageRerankModel:       cfg.GetAITaggingVLMVoyageRerankModel(),
		TaggingVLMVoyageRerankTopK:        cfg.GetAITaggingVLMVoyageRerankTopK(),
		TaggingVLMVoyageEndpoint:          cfg.GetAITaggingVLMVoyageEndpoint(),
		TaggingVLMVoyageVideoEnabled:      cfg.GetAITaggingVLMVoyageVideoEnabled(),
		TaggingVLMVoyageVideoModel:        cfg.GetAITaggingVLMVoyageVideoModel(),
		TaggingVLMVoyageSegmentSecs:       cfg.GetAITaggingVLMVoyageSegmentSecs(),
		TaggingVLMVoyageDimension:         cfg.GetAITaggingVLMVoyageDimension(),
		TaggingVLMVoyageEmbeddingEndpoint: cfg.GetAITaggingVLMVoyageEmbeddingEndpoint(),
		TaggingTaxonomyEndpoint:           cfg.GetAITaggingTaxonomyEndpoint(),
		TaggingTaxonomyAPIKeySet:          cfg.GetAITaggingTaxonomyAPIKey() != "",
		TaggingTaxonomyCategories:         cfg.GetAITaggingTaxonomyCategories(),
		TaggingTaxonomyMaxCandidates:      cfg.GetAITaggingTaxonomyMaxCandidates(),
	}
}

func availableVLMModels() []string {
	pairs := assets.VisionPairs()
	ret := make([]string, len(pairs))
	for i, pair := range pairs {
		ret[i] = pair.Name
	}
	return ret
}

func availableVoyageRerankModels() []string {
	return append([]string(nil), availableVoyageRerankModelValues...)
}

func availableVoyageVideoModels() []string {
	return append([]string(nil), availableVoyageVideoModelValues...)
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
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
	textService := r.getOllamaService()
	textConfig := textService.GetConfig()
	hasLocalTextProvider := textConfig.Backend == ollama.BackendOpenAICompatible &&
		textService.IsAvailable(ctx)

	var schemaVersion, errMsg *string
	if health.SchemaVersion != "" {
		schemaVersion = &health.SchemaVersion
	}
	if health.Error != "" {
		errMsg = &health.Error
	}

	return &AIServerStatus{
		State: aiServerStateFromHealth(health.State),
		// Enabled reflects configuration (ai_enabled), not runtime state: a
		// server that failed to start while enabled must still report
		// enabled=true, or a settings-panel save meant to repair the failure
		// (e.g. after fixing the provider) would silently turn the whole
		// server off by round-tripping this value back through
		// configureAIServer. Ready is the separate "actually running" signal.
		Enabled:                     mgr.Config.GetAIEnabled(),
		Ready:                       srv.Ready(),
		HasVLMProvider:              taggingStatus.Provider == llamaprov.ProviderName && taggingStatus.Available,
		HasLocalTextProvider:        hasLocalTextProvider,
		AvailableVLMModels:          availableVLMModels(),
		AvailableVoyageRerankModels: availableVoyageRerankModels(),
		AvailableVoyageVideoModels:  availableVoyageVideoModels(),
		BackendVersion:              health.BackendVersion,
		SchemaVersion:               schemaVersion,
		Error:                       errMsg,
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
	tagger := mgr.AIServer.Tagging()
	voyageAvailable := tagger != nil && tagger.SupportsVoyageTagging()

	var message, remediation *string
	if voyageAvailable && !status.Available {
		value := "Voyage AI video tagging is ready."
		message = &value
	} else {
		if status.Message != "" {
			message = &status.Message
		}
		if status.Remediation != "" {
			remediation = &status.Remediation
		}
	}

	return &AITaggingStatus{
		Provider:        status.Provider,
		Available:       status.Available || voyageAvailable,
		LocalAvailable:  status.Available,
		VoyageAvailable: voyageAvailable,
		Message:         message,
		Remediation:     remediation,
	}, nil
}

func (r *queryResolver) AiServerTaxonomy(ctx context.Context) (*AIServerTaxonomyStatus, error) {
	service := manager.GetInstance().AIServer.Tagging()
	if service == nil {
		return &AIServerTaxonomyStatus{CandidatesByCategory: []string{}}, nil
	}
	status := service.TaxonomyStatus()
	return &AIServerTaxonomyStatus{
		Endpoint:             status.Endpoint,
		Entries:              status.Entries,
		RefreshedAt:          status.RefreshedAt,
		CandidatesByCategory: status.CandidatesByCategory,
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

func (r *queryResolver) AiTaggingSceneSupports(ctx context.Context, sceneID int, runID *int) ([]*AITaggingLabelSupport, error) {
	db := manager.GetInstance().AIServer.DB()
	if db == nil {
		return []*AITaggingLabelSupport{}, nil
	}

	var selectedRun int64
	if runID != nil {
		selectedRun = int64(*runID)
	}
	supports, err := db.GetSceneLabelSupports(ctx, llamaprov.ProviderName, sceneID, selectedRun)
	if err != nil {
		return nil, err
	}

	ret := make([]*AITaggingLabelSupport, 0, len(supports))
	for _, support := range supports {
		var stashID *string
		if support.StashID != "" {
			id := support.StashID
			stashID = &id
		}
		ret = append(ret, &AITaggingLabelSupport{
			Tag:       support.Tag,
			StashID:   stashID,
			Frames:    support.Frames,
			SpanCount: support.SpanCount,
			FirstAt:   support.FirstAt,
			LastAt:    support.LastAt,
		})
	}
	return ret, nil
}
