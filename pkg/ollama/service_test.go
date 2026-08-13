package ollama

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestOpenAICompatibleBackend(t *testing.T) {
	var chatRequest struct {
		Model              string              `json:"model"`
		Messages           []OllamaChatMessage `json:"messages"`
		Stream             bool                `json:"stream"`
		Temperature        float64             `json:"temperature"`
		MaxTokens          int                 `json:"max_tokens"`
		ChatTemplateKwargs map[string]bool     `json:"chat_template_kwargs"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"huihui-qwen3-8b"}]}`))
		case "/v1/chat/completions":
			if r.Method != http.MethodPost {
				t.Errorf("chat method = %s, want POST", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&chatRequest); err != nil {
				t.Errorf("decode chat request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"generated answer"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewService(&OllamaConfig{
		Backend: BackendOpenAICompatible,
		BaseURL: server.URL,
		Model:   "huihui-qwen3-8b",
		Timeout: 5000,
		Enabled: true,
	})
	ctx := context.Background()
	if !service.IsAvailable(ctx) {
		t.Fatal("OpenAI-compatible backend was not available")
	}
	models, err := service.GetModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(models, []string{"huihui-qwen3-8b"}) {
		t.Fatalf("models = %v", models)
	}
	response, err := service.Generate(ctx, "user prompt", "", "system prompt")
	if err != nil {
		t.Fatal(err)
	}
	if response != "generated answer" {
		t.Fatalf("response = %q", response)
	}
	if chatRequest.Model != "huihui-qwen3-8b" || chatRequest.Stream || chatRequest.Temperature != 0 || chatRequest.MaxTokens != 800 {
		t.Fatalf("chat request = %+v", chatRequest)
	}
	if !reflect.DeepEqual(chatRequest.ChatTemplateKwargs, map[string]bool{"enable_thinking": false}) {
		t.Fatalf("chat template kwargs = %v", chatRequest.ChatTemplateKwargs)
	}
	wantMessages := []OllamaChatMessage{{Role: "system", Content: "system prompt"}, {Role: "user", Content: "user prompt"}}
	if !reflect.DeepEqual(chatRequest.Messages, wantMessages) {
		t.Fatalf("messages = %+v, want %+v", chatRequest.Messages, wantMessages)
	}
	entry, err := service.ExplainWord(ctx, "word", "context", "en", "ollama")
	if err != nil {
		t.Fatal(err)
	}
	if entry.AISource != "llama.cpp" {
		t.Fatalf("AI source = %q, want llama.cpp", entry.AISource)
	}
}

func TestOpenAICompatibleStructuredCompletion(t *testing.T) {
	var request struct {
		MaxTokens          int             `json:"max_tokens"`
		ChatTemplateKwargs map[string]bool `json:"chat_template_kwargs"`
		ResponseFormat     struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"json_schema"`
		} `json:"response_format"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"name\":\"Alice\"}"}}]}`))
	}))
	defer server.Close()

	service := NewService(&OllamaConfig{
		Backend: BackendOpenAICompatible,
		BaseURL: server.URL,
		Model:   "huihui-qwen3-8b",
		Timeout: 5000,
		Enabled: true,
	})
	schema := json.RawMessage(`{"type":"object","properties":{"name":{"type":"string"}},"required":["name"],"additionalProperties":false}`)
	var target struct {
		Name string `json:"name"`
	}
	if err := service.CompleteJSON(context.Background(), "system", "user", "identity", schema, 64, &target); err != nil {
		t.Fatal(err)
	}
	if target.Name != "Alice" {
		t.Fatalf("target = %+v", target)
	}
	if request.MaxTokens != 64 || request.ResponseFormat.Type != "json_schema" ||
		request.ResponseFormat.JSONSchema.Name != "identity" || !request.ResponseFormat.JSONSchema.Strict ||
		!reflect.DeepEqual(request.ResponseFormat.JSONSchema.Schema, schema) {
		t.Fatalf("structured request = %+v", request)
	}
	if !reflect.DeepEqual(request.ChatTemplateKwargs, map[string]bool{"enable_thinking": false}) {
		t.Fatalf("chat template kwargs = %v", request.ChatTemplateKwargs)
	}
}
