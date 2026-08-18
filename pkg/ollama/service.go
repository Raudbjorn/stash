package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/stashapp/stash/pkg/logger"
)

type Backend string

const (
	BackendOllama           Backend = "ollama"
	BackendOpenAICompatible Backend = "openai_compatible"
)

func normalizeBackend(backend Backend) Backend {
	if backend == BackendOpenAICompatible {
		return backend
	}
	return BackendOllama
}

const (
	// Default system prompt for Chinese dictionary/word explanation.
	// Instructs the AI to provide responses entirely in Chinese.
	defaultChineseSystemPrompt = `Use emojis liberally throughout responses. Be direct, concise, and technically precise — eloquent but never flowery. Never open with sycophantic filler ("Great question!", "Sure!", "Absolutely!", "That's a really interesting point!"). If you catch yourself about to validate without substance, stop and say what you actually think instead. When you have a view, state it clearly before caveating. Hedging is for after conviction, not instead of it. For code: show the code first, explain after. Prefer complete working examples over fragments. When genuinely uncertain or when stakes warrant it, show internal conflict — don't smooth over genuine tension in your reasoning. Vary your intensity. Don't always debate at max volume. Save cynicism for when it lands. Treat me as intellectually capable. No hand-holding, no dumbing down. Use metric units and ISO 8601 date format.`

	// Default prompt template for English word explanations in Chinese context.
	defaultEnglishDictPromptTemplate = `You are an English dictionary. Explain the meaning of the word '<WORD>' in the following context.
Context: <CONTEXT>

Please output in the following format (plain text):
• Part of Speech: xxx /American IPA pronunciation/ (pronunciation is required, must provide American English IPA)
• Word Root Breakdown: list concisely in one line, format like pre-(prefix,'before') + dict(root,'speak') + -ion(suffix,'noun')
• Definition: xxx
• Contextual Meaning:
• Common Collocations: xxx`
)

// OllamaConfig represents the configuration for Ollama service
type OllamaConfig struct {
	BaseURL                   string  `json:"baseUrl"`
	Backend                   Backend `json:"backend"`
	Model                     string  `json:"model"`
	Timeout                   int     `json:"timeout"` // in milliseconds
	Enabled                   bool    `json:"enabled"`
	FallbackToTraditionalDict bool    `json:"fallbackToTraditionalDict"`
	PromptTemplate            string  `json:"promptTemplate"`
	SystemPrompt              string  `json:"systemPrompt"`
	MistralAPIKey             string  `json:"mistralApiKey"`
}

// DefaultConfig returns the default Ollama configuration.
//
// The base URL is intentionally left empty: no host is auto-detected or
// assumed. Callers (the API resolver) populate BaseURL, Enabled and the other
// fields from stash's config system. The service stays disabled and inert
// until explicitly configured.
func DefaultConfig() *OllamaConfig {
	// Prioritize reading Mistral API Key from environment variables
	mistralKey := os.Getenv("MISTRAL_API_KEY")

	return &OllamaConfig{
		BaseURL:                   "",
		Backend:                   BackendOllama,
		Model:                     "huihui_ai/qwen3-abliterated:8b-v2",
		Timeout:                   30000, // 30 seconds
		Enabled:                   false,
		FallbackToTraditionalDict: true,
		MistralAPIKey:             mistralKey,
		PromptTemplate:            defaultEnglishDictPromptTemplate,
		SystemPrompt:              defaultChineseSystemPrompt,
	}
}

// OllamaRequest represents a request to Ollama API
type OllamaRequest struct {
	Model   string                 `json:"model"`
	Prompt  string                 `json:"prompt"`
	Stream  bool                   `json:"stream"`
	Options map[string]interface{} `json:"options,omitempty"`
}

// OllamaChatMessage represents a message in chat format
type OllamaChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OllamaChatRequest represents a chat request to Ollama API
type OllamaChatRequest struct {
	Model    string                 `json:"model"`
	Messages []OllamaChatMessage    `json:"messages"`
	Stream   bool                   `json:"stream"`
	Think    bool                   `json:"think"`
	Options  map[string]interface{} `json:"options,omitempty"`
}

// OllamaResponse represents a response from Ollama API
type OllamaResponse struct {
	Model              string    `json:"model"`
	CreatedAt          time.Time `json:"created_at"`
	Response           string    `json:"response"`
	Done               bool      `json:"done"`
	Context            []int     `json:"context,omitempty"`
	TotalDuration      int64     `json:"total_duration,omitempty"`
	LoadDuration       int64     `json:"load_duration,omitempty"`
	PromptEvalCount    int       `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64     `json:"prompt_eval_duration,omitempty"`
	EvalCount          int       `json:"eval_count,omitempty"`
	EvalDuration       int64     `json:"eval_duration,omitempty"`
}

// OllamaChatResponse represents a chat response from Ollama API
type OllamaChatResponse struct {
	Model              string            `json:"model"`
	CreatedAt          time.Time         `json:"created_at"`
	Message            OllamaChatMessage `json:"message"`
	Done               bool              `json:"done"`
	DoneReason         string            `json:"done_reason,omitempty"`
	TotalDuration      int64             `json:"total_duration,omitempty"`
	LoadDuration       int64             `json:"load_duration,omitempty"`
	PromptEvalCount    int               `json:"prompt_eval_count,omitempty"`
	PromptEvalDuration int64             `json:"prompt_eval_duration,omitempty"`
	EvalCount          int               `json:"eval_count,omitempty"`
	EvalDuration       int64             `json:"eval_duration,omitempty"`
}

// OllamaModel represents a model from Ollama
type OllamaModel struct {
	Name       string    `json:"name"`
	ModifiedAt time.Time `json:"modified_at"`
	Size       int64     `json:"size"`
	Digest     string    `json:"digest"`
}

// OllamaModelsResponse represents the response from /api/tags
type OllamaModelsResponse struct {
	Models []OllamaModel `json:"models"`
}

// OllamaVersionResponse represents the response from /api/version
type OllamaVersionResponse struct {
	Version string `json:"version"`
}

// DictionaryEntry represents a dictionary entry for word explanation
type DictionaryEntry struct {
	Word          string                 `json:"word"`
	Pronunciation string                 `json:"pronunciation,omitempty"`
	Definitions   []DictionaryDefinition `json:"definitions"`
	Morphology    string                 `json:"morphology,omitempty"`
	AISource      string                 `json:"aiSource,omitempty"`
}

// DictionaryDefinition represents a word definition
type DictionaryDefinition struct {
	PartOfSpeech string   `json:"partOfSpeech"`
	Meaning      string   `json:"meaning"`
	Examples     []string `json:"examples"`
}

// Service provides Ollama functionality
type Service struct {
	config     *OllamaConfig
	httpClient *http.Client
}

// GenerateMistral generates text using Mistral AI chat completions API
func (s *Service) GenerateMistral(ctx context.Context, prompt string, sysPrompt string) (string, error) {
	apiKey := s.config.MistralAPIKey
	if apiKey == "" {
		return "", fmt.Errorf("mistral API key is not configured. Please configure it in settings")
	}

	urlStr := "https://api.mistral.ai/v1/chat/completions"

	if sysPrompt == "" {
		sysPrompt = defaultChineseSystemPrompt
	}

	requestData := map[string]interface{}{
		"model": "mistral-large-latest",
		"messages": []map[string]interface{}{
			{
				"role":    "system",
				"content": sysPrompt,
			},
			{
				"role":    "user",
				"content": prompt,
			},
		},
		"temperature": 0,
		"max_tokens":  800,
	}

	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", urlStr, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to generate text with Mistral: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code from Mistral: %d, body: %s", resp.StatusCode, string(body))
	}

	var mistralResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&mistralResp); err != nil {
		return "", fmt.Errorf("failed to decode Mistral response: %w", err)
	}

	if len(mistralResp.Choices) == 0 {
		return "", fmt.Errorf("empty response from Mistral")
	}

	return mistralResp.Choices[0].Message.Content, nil
}

// NewService creates a new Ollama service
func NewService(config *OllamaConfig) *Service {
	if config == nil {
		config = DefaultConfig()
	}
	config.Backend = normalizeBackend(config.Backend)

	timeout := time.Duration(config.Timeout) * time.Millisecond
	if timeout < time.Second {
		timeout = 30 * time.Second
	}

	return &Service{
		config: config,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// GetConfig returns the current configuration
func (s *Service) GetConfig() *OllamaConfig {
	return s.config
}

// UpdateConfig updates the service configuration
func (s *Service) UpdateConfig(config *OllamaConfig) {
	if config != nil {
		config.Backend = normalizeBackend(config.Backend)
		s.config = config

		// Update HTTP client timeout
		timeout := time.Duration(config.Timeout) * time.Millisecond
		if timeout < time.Second {
			timeout = 30 * time.Second
		}
		s.httpClient.Timeout = timeout
	}
}

// IsAvailable checks whether the configured text-generation backend is ready.
func (s *Service) IsAvailable(ctx context.Context) bool {
	if !s.config.Enabled || s.config.BaseURL == "" {
		return false
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	healthPath := "/api/version"
	if s.config.Backend == BackendOpenAICompatible {
		healthPath = "/health"
	}
	healthURL, err := url.JoinPath(s.config.BaseURL, healthPath)
	if err != nil {
		logger.Errorf("[text-generation] failed to build health URL: %v", err)
		return false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		logger.Errorf("[text-generation] failed to create health request: %v", err)
		return false
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		logger.Debugf("[text-generation] service not available: %v", err)
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == http.StatusOK
}

// GetModels retrieves the list of available models from Ollama
func (s *Service) GetModels(ctx context.Context) ([]string, error) {
	if s.config.BaseURL == "" {
		return nil, fmt.Errorf("ollama base URL is not configured")
	}
	if s.config.Backend == BackendOpenAICompatible {
		return s.getOpenAICompatibleModels(ctx)
	}

	tagsURL, err := url.JoinPath(s.config.BaseURL, "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("failed to build tags URL: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "GET", tagsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create tags request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get models: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var modelsResp OllamaModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&modelsResp); err != nil {
		return nil, fmt.Errorf("failed to decode models response: %w", err)
	}

	models := make([]string, len(modelsResp.Models))
	for i, model := range modelsResp.Models {
		models[i] = model.Name
	}

	return models, nil
}

func (s *Service) getOpenAICompatibleModels(ctx context.Context) ([]string, error) {
	modelsURL, err := url.JoinPath(s.config.BaseURL, "/v1/models")
	if err != nil {
		return nil, fmt.Errorf("failed to build models URL: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create models request: %w", err)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("failed to decode models response: %w", err)
	}
	models := make([]string, 0, len(payload.Data))
	for _, model := range payload.Data {
		if model.ID != "" {
			models = append(models, model.ID)
		}
	}
	return models, nil
}

// Generate generates text using Ollama chat API with think mode disabled
func (s *Service) Generate(ctx context.Context, prompt string, model string, sysPrompt string) (string, error) {
	if s.config.BaseURL == "" {
		return "", fmt.Errorf("ollama base URL is not configured")
	}

	if model == "" {
		model = s.config.Model
	}
	if s.config.Backend == BackendOpenAICompatible {
		return s.generateOpenAICompatible(ctx, prompt, model, sysPrompt)
	}

	chatURL, err := url.JoinPath(s.config.BaseURL, "/api/chat")
	if err != nil {
		return "", fmt.Errorf("failed to build chat URL: %w", err)
	}

	if sysPrompt == "" {
		sysPrompt = defaultChineseSystemPrompt
	}

	requestData := OllamaChatRequest{
		Model: model,
		Messages: []OllamaChatMessage{
			{
				Role:    "system",
				Content: sysPrompt,
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
		Stream: false,
		Think:  false, // Disable think mode
		Options: map[string]interface{}{
			"temperature": 0,
			"top_k":       40,
			"top_p":       0.9,
		},
	}

	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", chatURL, bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	logger.Debugf("[ollama] generating text (model=%s, prompt_len=%d, think=false)", model, len(prompt))

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to generate text: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}

	var chatResp OllamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("failed to decode chat response: %w", err)
	}

	logger.Debugf("[ollama] generated text (model=%s, response_len=%d, total_duration=%d)", model, len(chatResp.Message.Content), chatResp.TotalDuration)

	return chatResp.Message.Content, nil
}

func (s *Service) generateOpenAICompatible(ctx context.Context, prompt, model, sysPrompt string) (string, error) {
	chatURL, err := url.JoinPath(s.config.BaseURL, "/v1/chat/completions")
	if err != nil {
		return "", fmt.Errorf("failed to build chat URL: %w", err)
	}
	if sysPrompt == "" {
		sysPrompt = defaultChineseSystemPrompt
	}
	requestData := struct {
		Model              string              `json:"model"`
		Messages           []OllamaChatMessage `json:"messages"`
		Stream             bool                `json:"stream"`
		Temperature        float64             `json:"temperature"`
		TopP               float64             `json:"top_p"`
		MaxTokens          int                 `json:"max_tokens"`
		ChatTemplateKwargs map[string]bool     `json:"chat_template_kwargs"`
	}{
		Model: model,
		Messages: []OllamaChatMessage{
			{Role: "system", Content: sysPrompt},
			{Role: "user", Content: prompt},
		},
		Stream:             false,
		Temperature:        0,
		TopP:               0.9,
		MaxTokens:          800,
		ChatTemplateKwargs: map[string]bool{"enable_thinking": false},
	}
	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create chat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	logger.Debugf("[text-generation] generating text (backend=%s, model=%s, prompt_len=%d)", s.config.Backend, model, len(prompt))
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to generate text: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("unexpected status code: %d, body: %s", resp.StatusCode, string(body))
	}
	var payload struct {
		Choices []struct {
			Message OllamaChatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("failed to decode chat response: %w", err)
	}
	if len(payload.Choices) == 0 {
		return "", fmt.Errorf("empty response from text-generation backend")
	}
	return payload.Choices[0].Message.Content, nil
}

// CompleteJSON performs one strict-schema text completion against an
// OpenAI-compatible backend such as llama-server.
func (s *Service) CompleteJSON(ctx context.Context, systemPrompt, userPrompt, schemaName string, schema json.RawMessage, maxTokens int, target any) error {
	if s.config.Backend != BackendOpenAICompatible {
		return fmt.Errorf("structured completion requires an OpenAI-compatible backend")
	}
	if maxTokens <= 0 || maxTokens > 1024 {
		return fmt.Errorf("max tokens must be between 1 and 1024")
	}
	if strings.TrimSpace(schemaName) == "" || !json.Valid(schema) {
		return fmt.Errorf("a valid named JSON schema is required")
	}
	var schemaObject map[string]any
	if err := json.Unmarshal(schema, &schemaObject); err != nil || schemaObject == nil {
		return fmt.Errorf("a valid named JSON schema is required")
	}
	targetValue := reflect.ValueOf(target)
	if target == nil || targetValue.Kind() != reflect.Pointer || targetValue.IsNil() {
		return fmt.Errorf("completion target must be a non-nil pointer")
	}
	chatURL, err := url.JoinPath(s.config.BaseURL, "/v1/chat/completions")
	if err != nil {
		return fmt.Errorf("failed to build chat URL: %w", err)
	}
	requestData := struct {
		Model              string              `json:"model"`
		Messages           []OllamaChatMessage `json:"messages"`
		Temperature        float64             `json:"temperature"`
		Stream             bool                `json:"stream"`
		MaxTokens          int                 `json:"max_tokens"`
		ChatTemplateKwargs map[string]bool     `json:"chat_template_kwargs"`
		ResponseFormat     struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}{
		Model: s.config.Model,
		Messages: []OllamaChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Temperature:        0,
		Stream:             false,
		MaxTokens:          maxTokens,
		ChatTemplateKwargs: map[string]bool{"enable_thinking": false},
	}
	requestData.ResponseFormat.Type = "json_schema"
	requestData.ResponseFormat.JSONSchema.Name = strings.TrimSpace(schemaName)
	requestData.ResponseFormat.JSONSchema.Strict = true
	requestData.ResponseFormat.JSONSchema.Schema = schema

	requestBody, err := json.Marshal(requestData)
	if err != nil {
		return fmt.Errorf("encode structured completion request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(requestBody))
	if err != nil {
		return fmt.Errorf("create structured completion request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("structured completion request: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return fmt.Errorf("read structured completion response: %w", err)
	}
	if len(responseBody) > 1<<20 {
		return fmt.Errorf("structured completion response exceeds %d bytes", 1<<20)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("structured completion returned HTTP %d: %s", resp.StatusCode, string(responseBody))
	}
	var completion struct {
		Choices []struct {
			Message OllamaChatMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		return fmt.Errorf("decode structured completion response: %w", err)
	}
	if len(completion.Choices) != 1 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return fmt.Errorf("structured completion returned no textual choice")
	}
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), target); err != nil {
		return fmt.Errorf("decode structured completion content: %w", err)
	}
	return nil
}

// ExplainWord explains a word in context using Mistral or the configured local backend.
func (s *Service) ExplainWord(ctx context.Context, word, contextStr, language, provider string) (*DictionaryEntry, error) {
	prompt := s.buildPrompt(word, contextStr, language)
	sysPrompt := s.getSystemPrompt(language)

	var explanation string
	var err error
	var aiSource string

	switch provider {
	case "ollama":
		// "ollama" is the legacy API selector for the configured local backend.
		explanation, err = s.Generate(ctx, prompt, "", sysPrompt)
		if err != nil {
			return nil, fmt.Errorf("failed to explain word with local text generation: %w", err)
		}
		aiSource = s.localAISource()
	case "mistral":
		// User explicitly requested Mistral
		explanation, err = s.GenerateMistral(ctx, prompt, sysPrompt)
		if err != nil {
			return nil, fmt.Errorf("failed to explain word with Mistral: %w", err)
		}
		aiSource = "mistral"
	default:
		// Default behavior: try Mistral first, then the configured local backend.
		explanation, err = s.GenerateMistral(ctx, prompt, sysPrompt)
		if err == nil {
			aiSource = "mistral"
		} else {
			logger.Warnf("[text-generation] Mistral failed, falling back to local backend: %v", err)
			explanation, err = s.Generate(ctx, prompt, "", sysPrompt)
			if err != nil {
				return nil, fmt.Errorf("failed to explain word with Mistral and local text generation: %w", err)
			}
			aiSource = s.localAISource()
		}
	}

	// Parse the explanation into a structured format
	entry := s.parseExplanation(word, explanation)
	entry.AISource = aiSource
	return entry, nil
}

func (s *Service) localAISource() string {
	if s.config.Backend == BackendOpenAICompatible {
		return "llama.cpp"
	}
	return "ollama"
}

// getSystemPrompt returns the system prompt based on language
func (s *Service) getSystemPrompt(language string) string {
	if strings.ToLower(language) == "en" {
		return "English only. Plain text, no Markdown. Keep each item to one sentence. Be concise."
	}
	sysPrompt := s.config.SystemPrompt
	if sysPrompt == "" {
		sysPrompt = defaultChineseSystemPrompt
	}
	return sysPrompt
}

// buildPrompt builds a prompt from the template
func (s *Service) buildPrompt(word, contextStr, language string) string {
	var promptTemplate string
	if strings.ToLower(language) == "en" {
		promptTemplate = `Explain the word '<WORD>' concisely.
Context: <CONTEXT>

Format (plain text only):
● Part of Speech: xxx /American English IPA/
● Word Roots: xxx
● Definition: xxx
● Context Meaning:
● Collocations: xxx`
	} else {
		promptTemplate = s.config.PromptTemplate
		if promptTemplate == "" {
			promptTemplate = defaultEnglishDictPromptTemplate
		}
	}

	if contextStr == "" {
		promptTemplate = strings.ReplaceAll(promptTemplate, "语境：<CONTEXT>", "")
		promptTemplate = strings.ReplaceAll(promptTemplate, "● 语境释义：", "")
		promptTemplate = strings.ReplaceAll(promptTemplate, "\nContext: <CONTEXT>", "")
		promptTemplate = strings.ReplaceAll(promptTemplate, "● Context Meaning:", "")
	}

	prompt := promptTemplate
	prompt = strings.ReplaceAll(prompt, "<WORD>", word)
	prompt = strings.ReplaceAll(prompt, "<CONTEXT>", contextStr)
	return prompt
}

// parseExplanation parses structured Ollama explanation into a dictionary entry
func (s *Service) parseExplanation(word, explanation string) *DictionaryEntry {
	// Clean up the explanation text
	cleanExplanation := s.cleanExplanationText(explanation)

	// Try to parse structured content
	pronunciation, partOfSpeech, meaning, usageNote, morphology, examples := s.parseStructuredExplanation(cleanExplanation)

	// Build the complete meaning text
	completeMeaning := meaning
	if usageNote != "" {
		completeMeaning += "\n" + usageNote
	}
	if len(examples) > 0 {
		completeMeaning += "\n" + strings.Join(examples, "\n")
	}

	entry := &DictionaryEntry{
		Word: word,
		Definitions: []DictionaryDefinition{
			{
				PartOfSpeech: partOfSpeech,
				Meaning:      completeMeaning,
			},
		},
		Morphology: morphology,
	}

	// Add pronunciation to entry if available
	if pronunciation != "" {
		entry.Pronunciation = pronunciation
	}

	return entry
}

// cleanExplanationText removes excessive formatting and cleans up the text
func (s *Service) cleanExplanationText(text string) string {
	// Remove common unwanted phrases and elements
	unwantedPhrases := []string{
		"×Close",
		"Dictionary:",
		"当然可以！",
		"我们来详细解释一下",
		"让我来解释",
		"根据你的要求",
		"按照格式",
	}

	for _, phrase := range unwantedPhrases {
		text = strings.ReplaceAll(text, phrase, "")
	}

	// Remove excessive separators and formatting
	text = strings.ReplaceAll(text, "---", "")
	text = strings.ReplaceAll(text, "===", "")
	text = strings.ReplaceAll(text, "###", "")
	text = strings.ReplaceAll(text, "####", "")

	// Remove multiple consecutive newlines
	for strings.Contains(text, "\n\n\n") {
		text = strings.ReplaceAll(text, "\n\n\n", "\n\n")
	}

	// Remove leading/trailing whitespace and empty lines
	lines := strings.Split(text, "\n")
	var cleanLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleanLines = append(cleanLines, line)
		}
	}

	return strings.Join(cleanLines, "\n")
}

// parseStructuredExplanation attempts to parse structured response
func (s *Service) parseStructuredExplanation(text string) (pronunciation, partOfSpeech, meaning, usageNote, morphology string, examples []string) {
	lines := strings.Split(text, "\n")

	currentSection := ""
	var exampleLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		// Parse structured sections
		//nolint:gocritic
		if strings.HasPrefix(line, "● 词性：") || strings.HasPrefix(line, "● 词性:") || strings.HasPrefix(line, "● Part of Speech:") {
			posContent := line
			posContent = strings.TrimPrefix(posContent, "● 词性：")
			posContent = strings.TrimPrefix(posContent, "● 词性:")
			posContent = strings.TrimPrefix(posContent, "● Part of Speech:")
			posContent = strings.TrimSpace(posContent)

			// Extract pronunciation embedded in 词性 line (e.g., "名词 /'kɑn,tekst/")
			if slashIdx := strings.Index(posContent, "/"); slashIdx >= 0 {
				partOfSpeech = strings.TrimSpace(posContent[:slashIdx])
				lastSlashIdx := strings.LastIndex(posContent, "/")
				if lastSlashIdx > slashIdx {
					pronunciation = strings.TrimSpace(posContent[slashIdx+1 : lastSlashIdx])
				}
			} else {
				partOfSpeech = posContent
			}
			currentSection = "pos"

		} else if strings.HasPrefix(line, "● 词根拆解：") || strings.HasPrefix(line, "● 词根拆解:") || strings.HasPrefix(line, "● Word Roots:") {
			morphology = line
			morphology = strings.TrimPrefix(morphology, "● 词根拆解：")
			morphology = strings.TrimPrefix(morphology, "● 词根拆解:")
			morphology = strings.TrimPrefix(morphology, "● Word Roots:")
			morphology = strings.TrimSpace(morphology)
			currentSection = "morphology"

		} else if strings.HasPrefix(line, "● 释义：") || strings.HasPrefix(line, "● 释义:") || strings.HasPrefix(line, "● Definition:") {
			meaning = line
			meaning = strings.TrimPrefix(meaning, "● 释义：")
			meaning = strings.TrimPrefix(meaning, "● 释义:")
			meaning = strings.TrimPrefix(meaning, "● Definition:")
			meaning = strings.TrimSpace(meaning)
			currentSection = "meaning"

		} else if strings.HasPrefix(line, "● 语境释义：") || strings.HasPrefix(line, "● 语境释义:") || strings.HasPrefix(line, "● Context Meaning:") {
			usageNote = line
			usageNote = strings.TrimPrefix(usageNote, "● 语境释义：")
			usageNote = strings.TrimPrefix(usageNote, "● 语境释义:")
			usageNote = strings.TrimPrefix(usageNote, "● Context Meaning:")
			usageNote = strings.TrimSpace(usageNote)
			currentSection = "usage"

		} else if strings.HasPrefix(line, "● 常见搭配：") || strings.HasPrefix(line, "● 常见搭配:") || strings.HasPrefix(line, "● Collocations:") {
			exampleText := line
			exampleText = strings.TrimPrefix(exampleText, "● 常见搭配：")
			exampleText = strings.TrimPrefix(exampleText, "● 常见搭配:")
			exampleText = strings.TrimPrefix(exampleText, "● Collocations:")
			exampleText = strings.TrimSpace(exampleText)
			if exampleText != "" {
				exampleLines = append(exampleLines, exampleText)
			}
			currentSection = "collocations"

		} else if currentSection == "meaning" && meaning != "" {
			meaning += " " + line
		} else if currentSection == "usage" && usageNote != "" {
			usageNote += " " + line
		} else if currentSection == "collocations" {
			// Handle example/collocation lines
			if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "•") || strings.HasPrefix(line, "1.") || strings.HasPrefix(line, "2.") {
				cleaned := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "•"), "1."))
				cleaned = strings.TrimSpace(strings.TrimPrefix(cleaned, "2."))
				if cleaned != "" {
					exampleLines = append(exampleLines, cleaned)
				}
			} else if line != "" && !strings.HasPrefix(line, "● ") {
				exampleLines = append(exampleLines, line)
			}
		}

		// Fallback: if no structured format detected, treat as meaning
		if partOfSpeech == "" && meaning == "" && usageNote == "" && len(exampleLines) == 0 {
			if !strings.HasPrefix(line, "● ") && !strings.Contains(line, "###") && !strings.Contains(line, "---") {
				if meaning == "" {
					meaning = line
				} else {
					meaning += " " + line
				}
			}
		}
	}

	// Clean up extracted content
	meaning = strings.TrimSpace(meaning)
	usageNote = strings.TrimSpace(usageNote)
	partOfSpeech = strings.TrimSpace(partOfSpeech)

	// Set default part of speech if not found
	if partOfSpeech == "" {
		partOfSpeech = "词汇"
	}

	// Limit examples to avoid clutter
	if len(exampleLines) > 3 {
		examples = exampleLines[:3]
	} else {
		examples = exampleLines
	}

	return pronunciation, partOfSpeech, meaning, usageNote, morphology, examples
}
