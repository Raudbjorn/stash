package llamaprov

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/stashapp/stash/pkg/aitag"
)

const maxResponseBytes = 1 << 20

type completionRequest struct {
	Model          string         `json:"model"`
	Messages       []message      `json:"messages"`
	Temperature    float64        `json:"temperature"`
	Stream         bool           `json:"stream"`
	CachePrompt    bool           `json:"cache_prompt"`
	MaxTokens      int            `json:"max_tokens"`
	ResponseFormat responseFormat `json:"response_format"`
}

type message struct {
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

type responseFormat struct {
	Type       string     `json:"type"`
	JSONSchema jsonSchema `json:"json_schema"`
}

type jsonSchema struct {
	Name   string          `json:"name"`
	Strict bool            `json:"strict"`
	Schema json.RawMessage `json:"schema"`
}

type completionResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// ClassifyFrame returns one complete yes/no decision map for a raw RGB frame.
func (p *Provider) ClassifyFrame(ctx context.Context, rgb []byte, width, height int) (map[string]bool, error) {
	if err := p.waitUntilReady(ctx); err != nil {
		return nil, err
	}
	return p.classifyFrame(ctx, rgb, width, height)
}

// CompleteJSON performs one bounded strict-schema text completion.
func (p *Provider) CompleteJSON(ctx context.Context, systemPrompt, userPrompt, schemaName string, schema json.RawMessage, maxTokens int, target any) error {
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
	if target == nil {
		return fmt.Errorf("completion target is required")
	}
	targetValue := reflect.ValueOf(target)
	if targetValue.Kind() != reflect.Pointer || targetValue.IsNil() {
		return fmt.Errorf("completion target must be a non-nil pointer")
	}
	if err := p.waitUntilReady(ctx); err != nil {
		return err
	}
	payload := completionRequest{
		Model: p.pair.Name,
		Messages: []message{
			{Role: "system", Content: []contentPart{{Type: "text", Text: systemPrompt}}},
			{Role: "user", Content: []contentPart{{Type: "text", Text: userPrompt}}},
		},
		Temperature: 0,
		Stream:      false,
		CachePrompt: true,
		MaxTokens:   maxTokens,
		ResponseFormat: responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchema{
				Name: strings.TrimSpace(schemaName), Strict: true, Schema: schema,
			},
		},
	}
	content, err := p.complete(ctx, payload)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("decode llama VLM completion: %w", err)
	}
	return nil
}

func (p *Provider) waitUntilReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.waitReady != nil {
		return p.waitReady(ctx)
	}
	return nil
}

func (p *Provider) classifyFrame(ctx context.Context, rgb []byte, width, height int) (map[string]bool, error) {
	png, err := aitag.EncodePNG(rgb, width, height)
	if err != nil {
		return nil, err
	}
	dataURL := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
	payload := completionRequest{
		Model: p.pair.Name,
		Messages: []message{{
			Role: "user",
			Content: []contentPart{
				{Type: "text", Text: p.template.prompt},
				{Type: "image_url", ImageURL: &imageURL{URL: dataURL}},
			},
		}},
		Temperature: 0,
		Stream:      false,
		CachePrompt: true,
		MaxTokens:   p.template.maxTokens,
		ResponseFormat: responseFormat{
			Type: "json_schema",
			JSONSchema: jsonSchema{
				Name: "frame_labels", Strict: true, Schema: p.template.schema,
			},
		},
	}
	content, err := p.complete(ctx, payload)
	if err != nil {
		return nil, err
	}
	return p.parseDecisions(content)
}

func (p *Provider) complete(ctx context.Context, payload completionRequest) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("llama VLM request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := readCapped(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, parseServerError(response.StatusCode, responseBody)
	}
	var completion completionResponse
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		return nil, fmt.Errorf("decode llama VLM response: %w", err)
	}
	if len(completion.Choices) != 1 {
		return nil, fmt.Errorf("llama VLM returned %d choices, want exactly one", len(completion.Choices))
	}
	var content string
	if err := json.Unmarshal(completion.Choices[0].Message.Content, &content); err != nil || strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("llama VLM choice has no textual content")
	}
	return []byte(content), nil
}

func (p *Provider) parseDecisions(content []byte) (map[string]bool, error) {
	var decoded map[string]string
	if err := json.Unmarshal(content, &decoded); err != nil {
		return nil, fmt.Errorf("decode llama VLM decisions: %w", err)
	}
	if len(decoded) != len(p.labels) {
		return nil, fmt.Errorf("llama VLM returned %d labels, want %d", len(decoded), len(p.labels))
	}
	decisions := make(map[string]bool, len(p.labels))
	for _, label := range p.labels {
		decision, ok := decoded[label]
		if !ok {
			return nil, fmt.Errorf("llama VLM response is missing label %q", label)
		}
		switch decision {
		case "yes":
			decisions[label] = true
		case "no":
			decisions[label] = false
		default:
			return nil, fmt.Errorf("llama VLM returned %q for %q, want yes or no", decision, label)
		}
	}
	return decisions, nil
}

func readCapped(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("llama-server response exceeds %d bytes", maxResponseBytes)
	}
	return body, nil
}

func parseServerError(status int, body []byte) error {
	var envelope struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Message string          `json:"message"`
			Type    string          `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		code := strings.Trim(string(envelope.Error.Code), `"`)
		return fmt.Errorf("llama-server HTTP %d (%s/%s): %s",
			status, envelope.Error.Type, code, envelope.Error.Message)
	}
	return fmt.Errorf("llama-server HTTP %d: %s", status, strings.TrimSpace(string(body)))
}
