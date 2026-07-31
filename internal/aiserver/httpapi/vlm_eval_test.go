package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/tagging"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/aitag/llamaprov"
	"github.com/stashapp/stash/pkg/models"
)

type evalHTTPProvider struct{ name string }

func (p *evalHTTPProvider) Name() string                  { return p.name }
func (*evalHTTPProvider) Capabilities() aitag.Capability  { return aitag.CapVideo | aitag.CapImages }
func (*evalHTTPProvider) Available(context.Context) error { return nil }
func (*evalHTTPProvider) Models(context.Context) ([]aitag.ModelInfo, error) {
	return []aitag.ModelInfo{{Name: "pair", Type: "vision-language"}}, nil
}
func (*evalHTTPProvider) AnalyzeVideo(context.Context, string, aitag.Options, aitag.Sink) (*aitag.Result, error) {
	return nil, nil
}
func (*evalHTTPProvider) AnalyzeImages(context.Context, []string, aitag.Options) (*aitag.ImageResult, error) {
	return nil, nil
}
func (*evalHTTPProvider) Close() error     { return nil }
func (*evalHTTPProvider) Labels() []string { return []string{"tag"} }
func (*evalHTTPProvider) ClassifyFrame(context.Context, []byte, int, int) (map[string]bool, error) {
	return map[string]bool{"tag": true}, nil
}

type evalHTTPBackend struct {
	*testBackend
	tagger *tagging.Service
}

func (b *evalHTTPBackend) Tagging() *tagging.Service   { return b.tagger }
func (*evalHTTPBackend) Trainer() *tagging.Trainer     { return nil }
func (*evalHTTPBackend) TaggingStatus() tagging.Status { return tagging.Status{} }

func setupEvalHTTP(t *testing.T, handler action.Handler) (*Server, *testBackend, *evalHTTPProvider) {
	t.Helper()
	server, base := newTestServer(t)
	provider := &evalHTTPProvider{name: tagging.ProviderVLM}
	tagger := tagging.NewService(models.Repository{}, base.db, tagging.Config{Provider: provider})
	server.backend = &evalHTTPBackend{testBackend: base, tagger: tagger}
	registerAction(base, tagging.EvalActionID, tagging.ServiceName, []action.ContextRule{
		{Pages: []string{"scenes"}, Selection: action.SelectionSingle},
		{Pages: []string{"scenes"}, Selection: action.SelectionMulti},
	}, handler)
	return server, base, provider
}

func TestVLMEvalEndpointCanonicalizesAndQueuesLowPriority(t *testing.T) {
	result := &tagging.VLMEvalResult{
		Provider: tagging.ProviderVLM, Model: "pair", SceneIDs: []int{1, 3},
		FrameInterval: llamaprov.DefaultFrameInterval, Frames: 4,
		PerLabel: map[string]tagging.VLMEvalLabel{"tag": {Samples: 4}},
	}
	server, backend, _ := setupEvalHTTP(t, func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return result, nil
	})
	recorder := do(t, server, http.MethodPost, "/tagging/vlm/eval", map[string]any{
		"scene_ids": []int{3, 1, 3},
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	response := decode[map[string]any](t, recorder)
	if len(response) != 2 || response["task_id"] == "" || response["status"] != "queued" {
		t.Fatalf("response = %v", response)
	}
	taskID := response["task_id"].(string)
	record, err := backend.tasks.Get(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Priority.String() != "low" || !reflect.DeepEqual(record.Context.SelectedIDs, []string{"1", "3"}) || record.Context.IsDetailView {
		t.Errorf("record = %+v", record)
	}
	if record.Params["frame_interval"] != llamaprov.DefaultFrameInterval {
		t.Errorf("params = %v", record.Params)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !record.Status.Terminal() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		record, _ = backend.tasks.Get(taskID)
	}
	if !record.Status.Terminal() {
		t.Fatal("evaluation task did not complete")
	}
	got := do(t, server, http.MethodGet, "/tasks/"+taskID, nil)
	if got.Code != http.StatusOK || !bytes.Contains(got.Body.Bytes(), []byte(`"macro_f1":null`)) || !bytes.Contains(got.Body.Bytes(), []byte(`"per_label"`)) {
		t.Errorf("terminal task response %d: %s", got.Code, got.Body)
	}
}

func TestVLMEvalEndpointPublishesTerminalMetricsOverWebSocket(t *testing.T) {
	result := &tagging.VLMEvalResult{
		Provider: tagging.ProviderVLM, Model: "pair", SceneIDs: []int{1},
		FrameInterval: 30, Frames: 2,
		PerLabel: map[string]tagging.VLMEvalLabel{"tag": {Samples: 2, Measured: true}},
	}
	server, _, _ := setupEvalHTTP(t, func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		return result, nil
	})
	httpServer := httptest.NewServer(server.Routes())
	defer httpServer.Close()
	connection, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(httpServer.URL, "http")+"/ws/tasks", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	submitted := do(t, server, http.MethodPost, "/tagging/vlm/eval", map[string]any{"scene_ids": []int{1}})
	if submitted.Code != http.StatusOK {
		t.Fatalf("submit status=%d: %s", submitted.Code, submitted.Body)
	}
	_ = connection.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		_, raw, err := connection.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var event struct {
			Type string         `json:"type"`
			Task map[string]any `json:"task"`
		}
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "task.completed" {
			continue
		}
		terminal, ok := event.Task["result"].(map[string]any)
		if !ok || terminal["per_label"] == nil || terminal["macro_f1"] != nil {
			t.Fatalf("terminal evaluation event = %v", event.Task)
		}
		break
	}
}

func TestVLMEvalEndpointDeduplicatesCanonicalSelection(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	server, _, _ := setupEvalHTTP(t, func(ctx context.Context, _ action.ContextInput, _ map[string]any, _ action.Handle) (any, error) {
		once.Do(func() { close(started) })
		<-ctx.Done()
		return nil, ctx.Err()
	})
	first := do(t, server, http.MethodPost, "/tagging/vlm/eval", map[string]any{"scene_ids": []int{2, 1, 2}})
	if first.Code != http.StatusOK {
		t.Fatalf("first status=%d: %s", first.Code, first.Body)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first evaluation did not start")
	}
	second := do(t, server, http.MethodPost, "/tagging/vlm/eval", map[string]any{"scene_ids": []int{1, 2}, "frame_interval": 30})
	if second.Code != http.StatusConflict {
		t.Fatalf("second status=%d: %s", second.Code, second.Body)
	}
	var envelope struct {
		Detail struct {
			Code string `json:"code"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Detail.Code != "ACTION_ALREADY_IN_PROGRESS" {
		t.Errorf("duplicate code = %q", envelope.Detail.Code)
	}
}

func TestVLMEvalEndpointValidationStatuses(t *testing.T) {
	server, _, provider := setupEvalHTTP(t, okHandler)
	for name, body := range map[string]any{
		"empty scenes":      map[string]any{"scene_ids": []int{}},
		"invalid scene":     map[string]any{"scene_ids": []int{0}},
		"zero interval":     map[string]any{"scene_ids": []int{1}, "frame_interval": 0},
		"negative interval": map[string]any{"scene_ids": []int{1}, "frame_interval": -1},
	} {
		t.Run(name, func(t *testing.T) {
			recorder := do(t, server, http.MethodPost, "/tagging/vlm/eval", body)
			if recorder.Code != http.StatusBadRequest {
				t.Errorf("status=%d body=%s", recorder.Code, recorder.Body)
			}
		})
	}

	request := httptest.NewRequest(http.MethodPost, "/tagging/vlm/eval", bytes.NewBufferString(`{"scene_ids":`))
	recorder := httptest.NewRecorder()
	server.Routes().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Errorf("malformed status=%d body=%s", recorder.Code, recorder.Body)
	}

	provider.name = "native"
	recorder = do(t, server, http.MethodPost, "/tagging/vlm/eval", map[string]any{"scene_ids": []int{1}})
	assertVLMRequired(t, recorder)
	provider.name = tagging.ProviderVLM

	backend := server.backend.(*evalHTTPBackend)
	backend.ready = false
	recorder = do(t, server, http.MethodPost, "/tagging/vlm/eval", map[string]any{"scene_ids": []int{1}})
	assertVLMRequired(t, recorder)
}

func assertVLMRequired(t *testing.T, recorder *httptest.ResponseRecorder) {
	t.Helper()
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	var envelope struct {
		Detail struct {
			Code string `json:"code"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Detail.Code != "VLM_PROVIDER_REQUIRED" {
		t.Errorf("code = %q", envelope.Detail.Code)
	}
}
