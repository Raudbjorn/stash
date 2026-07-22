package api

import (
	"context"
)

// OllamaStatus returns the current Ollama configuration and status
func (r *queryResolver) OllamaStatus(ctx context.Context) (*OllamaStatus, error) {
	ollamaService := r.getOllamaService()

	config := ollamaService.GetConfig()
	available := ollamaService.IsAvailable(ctx)

	var models []string
	var version *string

	if available {
		if modelList, err := ollamaService.GetModels(ctx); err == nil {
			models = modelList
		}
		// We don't expose version for now, as it requires additional API call
	}

	return &OllamaStatus{
		Available: available,
		Config: &OllamaConfig{
			BaseURL:                   config.BaseURL,
			Model:                     config.Model,
			Timeout:                   config.Timeout,
			Enabled:                   config.Enabled,
			FallbackToTraditionalDict: config.FallbackToTraditionalDict,
			PromptTemplate:            config.PromptTemplate,
			MistralAPIKey:             &config.MistralAPIKey,
		},
		Models:  models,
		Version: version,
	}, nil
}

// OllamaModels returns the list of available Ollama models
func (r *queryResolver) OllamaModels(ctx context.Context) ([]string, error) {
	ollamaService := r.getOllamaService()

	if !ollamaService.IsAvailable(ctx) {
		return []string{}, nil
	}

	models, err := ollamaService.GetModels(ctx)
	if err != nil {
		return []string{}, err
	}

	return models, nil
}

// OllamaAvailable checks if the Ollama service is available
func (r *queryResolver) OllamaAvailable(ctx context.Context) (bool, error) {
	ollamaService := r.getOllamaService()
	return ollamaService.IsAvailable(ctx), nil
}
