package api

import (
	"context"

	"github.com/stashapp/stash/internal/manager/config"
)

// ConfigureOllama configures the Ollama service settings and persists them
// through stash's config system.
func (r *mutationResolver) ConfigureOllama(ctx context.Context, input OllamaConfigInput) (*OllamaConfig, error) {
	c := config.GetInstance()

	c.SetBool(config.OllamaEnabled, input.Enabled)
	c.SetString(config.OllamaBaseURL, input.BaseURL)
	c.SetString(config.OllamaModel, input.Model)
	c.SetInt(config.OllamaTimeout, input.Timeout)
	c.SetBool(config.OllamaFallbackToTraditionalDict, input.FallbackToTraditionalDict)
	if input.MistralAPIKey != nil {
		c.SetString(config.MistralAPIKey, *input.MistralAPIKey)
	}

	if err := c.Write(); err != nil {
		return nil, err
	}

	// rebuild the service from the freshly written config
	ollamaService := r.getOllamaService()
	cfg := ollamaService.GetConfig()

	return &OllamaConfig{
		BaseURL:                   cfg.BaseURL,
		Model:                     cfg.Model,
		Timeout:                   cfg.Timeout,
		Enabled:                   cfg.Enabled,
		FallbackToTraditionalDict: cfg.FallbackToTraditionalDict,
		PromptTemplate:            cfg.PromptTemplate,
		MistralAPIKeySet:          c.GetMistralAPIKeyConfigured(),
	}, nil
}

// OllamaGenerate generates text using Ollama
func (r *mutationResolver) OllamaGenerate(ctx context.Context, input OllamaGenerateInput) (*OllamaGenerateResult, error) {
	ollamaService := r.getOllamaService()

	model := input.Model
	if model == nil {
		m := ollamaService.GetConfig().Model
		model = &m
	}

	response, err := ollamaService.Generate(ctx, input.Prompt, *model, "")
	if err != nil {
		return nil, err
	}

	return &OllamaGenerateResult{
		Response: response,
		Model:    *model,
		// TotalDuration and EvalCount could be added if we enhance the service to return them
	}, nil
}

// OllamaExplainWord explains a word in context using Ollama
func (r *mutationResolver) OllamaExplainWord(ctx context.Context, input OllamaExplainWordInput) (*OllamaDictionaryEntry, error) {
	ollamaService := r.getOllamaService()

	language := "en"
	if input.Language != nil {
		language = *input.Language
	}

	var provider string
	if input.Provider != nil {
		provider = *input.Provider
	}

	entry, err := ollamaService.ExplainWord(ctx, input.Word, input.Context, language, provider)
	if err != nil {
		return nil, err
	}

	// Convert to GraphQL types
	definitions := make([]*OllamaDictionaryDefinition, len(entry.Definitions))
	for i, def := range entry.Definitions {
		definitions[i] = &OllamaDictionaryDefinition{
			PartOfSpeech: def.PartOfSpeech,
			Meaning:      def.Meaning,
			Examples:     def.Examples,
		}
	}

	return &OllamaDictionaryEntry{
		Word:          entry.Word,
		Pronunciation: &entry.Pronunciation,
		Definitions:   definitions,
		Morphology:    &entry.Morphology,
		AiSource:      &entry.AISource,
	}, nil
}
