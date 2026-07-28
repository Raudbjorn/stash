package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/interactions"
	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// ---------------------------------------------------------------- harness ---

type testBackend struct {
	ready        bool
	actions      *action.Registry
	tasks        *task.Manager
	db           *store.DB
	services     *service.Registry
	recommenders *recommend.Registry
}

func (b *testBackend) Ready() bool               { return b.ready }
func (b *testBackend) Actions() *action.Registry { return b.actions }
func (b *testBackend) Tasks() *task.Manager      { return b.tasks }
func (b *testBackend) DB() *store.DB             { return b.db }
func (b *testBackend) Recommenders() *recommend.Registry {
	if b.recommenders == nil {
		b.recommenders = recommend.NewRegistry()
	}
	return b.recommenders
}
func (b *testBackend) Interactions() *interactions.Service {
	if b.db == nil {
		return nil
	}
	return interactions.NewService(b.db)
}
func (b *testBackend) HealthSnapshot(context.Context) any { return map[string]any{"state": "ready"} }
func (b *testBackend) Version() VersionInfo {
	m := ">=0.8.0"
	return VersionInfo{BackendVersion: "0.9.3", FrontendMinVersion: &m, SchemaVersion: "0001"}
}

func newTestServer(t *testing.T) (*Server, *testBackend) {
	t.Helper()

	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "ai.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	services := service.NewRegistry()
	tasks := task.NewManager(task.Options{
		LoopInterval: 5 * time.Millisecond,
		Gate:         services,
	})
	tasks.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		tasks.Shutdown(ctx)
	})

	backend := &testBackend{
		ready:    true,
		actions:  action.NewRegistry(),
		tasks:    tasks,
		db:       db,
		services: services,
	}

	s := New(backend)
	t.Cleanup(s.Close)
	return s, backend
}

func do(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()

	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	// The shipped frontend still sends these; they must be ignored, not
	// rejected, now that Stash's session auth covers the route.
	req.Header.Set("x-ai-api-key", "legacy-value")

	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

func registerAction(b *testBackend, id, svc string, contexts []action.ContextRule, handler action.Handler) action.Definition {
	def := action.Definition{
		ID: id, Service: svc, Label: id,
		Contexts: contexts, DeduplicateSubmissions: true,
	}
	b.actions.Register(def, handler)
	b.services.Register(&service.Static{ServiceName: svc, Concurrency: 4})
	return def
}

func okHandler(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
	return map[string]any{"done": true}, nil
}

// ------------------------------------------------------------------ tests ---

func TestActionsAvailableUsesEmbeddedContext(t *testing.T) {
	s, b := newTestServer(t)
	registerAction(b, "tag", "svc", []action.ContextRule{{Selection: action.SelectionMulti}}, okHandler)

	// The body wraps the context in an object, as FastAPI's embed=True did.
	rec := do(t, s, "POST", "/actions/available", map[string]any{
		"context": map[string]any{"page": "scenes", "selectedIds": []string{"1"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	got := decode[[]map[string]any](t, rec)
	if len(got) != 1 || got[0]["id"] != "tag" {
		t.Fatalf("available = %v", got)
	}
	// Definition fields are snake_case, unlike the camelCase context.
	for _, key := range []string{"result_kind", "dialog_type", "input_schema", "deduplicate_submissions"} {
		if _, ok := got[0][key]; !ok {
			t.Errorf("definition missing %q", key)
		}
	}
}

func TestActionsAvailableEmitsEmptyArray(t *testing.T) {
	s, _ := newTestServer(t)

	rec := do(t, s, "POST", "/actions/available", map[string]any{
		"context": map[string]any{"page": "scenes"},
	})
	// Must be [] and not null, or the frontend's .map() throws.
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %s, want []", body)
	}
}

func TestActionSubmitInfersPriority(t *testing.T) {
	s, b := newTestServer(t)
	registerAction(b, "tag", "svc", nil, okHandler)

	// A detail view is interactive and jumps the queue.
	rec := do(t, s, "POST", "/actions/submit", map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes", "isDetailView": true, "entityId": "7"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[submitActionResponse](t, rec)
	if got.InferredPriority != "high" {
		t.Errorf("detail priority = %q, want high", got.InferredPriority)
	}
	if got.TaskID == "" || got.Status != "queued" {
		t.Errorf("response = %+v", got)
	}

	// A library view is background work. Distinct params keep each submission
	// clear of the previous one's deduplication fingerprint.
	rec = do(t, s, "POST", "/actions/submit", map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes"},
		"params":    map[string]any{"case": "library"},
	})
	if p := decode[submitActionResponse](t, rec).InferredPriority; p != "low" {
		t.Errorf("library priority = %q, want low", p)
	}

	// An explicit override wins.
	rec = do(t, s, "POST", "/actions/submit", map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes"},
		"params":    map[string]any{"case": "override"},
		"priority":  "normal",
	})
	if p := decode[submitActionResponse](t, rec).InferredPriority; p != "normal" {
		t.Errorf("override priority = %q, want normal", p)
	}
}

// The 409 body is a structured object the frontend reads field by field to show
// "already running" rather than an error.
func TestActionSubmitDuplicateReturns409(t *testing.T) {
	s, b := newTestServer(t)

	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	registerAction(b, "tag", "svc", nil, func(ctx context.Context, _ action.ContextInput, _ map[string]any, _ action.Handle) (any, error) {
		<-block
		return nil, nil
	})

	body := map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes", "selectedIds": []string{"1", "2"}},
		"params":    map[string]any{"threshold": 0.5},
	}

	first := do(t, s, "POST", "/actions/submit", body)
	if first.Code != http.StatusOK {
		t.Fatalf("first submit: %d %s", first.Code, first.Body)
	}
	firstID := decode[submitActionResponse](t, first).TaskID

	second := do(t, s, "POST", "/actions/submit", body)
	if second.Code != http.StatusConflict {
		t.Fatalf("second submit status = %d, want 409: %s", second.Code, second.Body)
	}

	wrapper := decode[map[string]json.RawMessage](t, second)
	var detail map[string]any
	if err := json.Unmarshal(wrapper["detail"], &detail); err != nil {
		t.Fatalf("detail is not an object: %s", second.Body)
	}
	if detail["code"] != "ACTION_ALREADY_IN_PROGRESS" {
		t.Errorf("code = %v", detail["code"])
	}
	if detail["task_id"] != firstID {
		t.Errorf("task_id = %v, want %v", detail["task_id"], firstID)
	}
	if detail["message"] == "" || detail["status"] == "" {
		t.Errorf("detail incomplete: %v", detail)
	}
}

func TestActionSubmitUnknownAndInapplicable(t *testing.T) {
	s, b := newTestServer(t)
	registerAction(b, "detail-only", "svc",
		[]action.ContextRule{{Selection: action.SelectionSingle}}, okHandler)

	rec := do(t, s, "POST", "/actions/submit", map[string]any{
		"action_id": "nope",
		"context":   map[string]any{"page": "scenes"},
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown action status = %d, want 404", rec.Code)
	}
	// FastAPI's error envelope.
	if d := decode[map[string]any](t, rec)["detail"]; d != "Action not found" {
		t.Errorf("detail = %v", d)
	}

	// Registered but not applicable here: Resolve finds nothing, so 404.
	rec = do(t, s, "POST", "/actions/submit", map[string]any{
		"action_id": "detail-only",
		"context":   map[string]any{"page": "scenes"},
	})
	if rec.Code != http.StatusNotFound {
		t.Errorf("inapplicable action status = %d, want 404", rec.Code)
	}
}

func TestTaskLifecycleEndpoints(t *testing.T) {
	s, b := newTestServer(t)
	registerAction(b, "tag", "svc", nil, okHandler)

	rec := do(t, s, "POST", "/tasks/submit", map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}
	id := decode[submitTaskResponse](t, rec).TaskID

	// GET /tasks/{id}
	deadline := time.Now().Add(3 * time.Second)
	var got taskResponse
	for time.Now().Before(deadline) {
		r := do(t, s, "GET", "/tasks/"+id, nil)
		if r.Code != http.StatusOK {
			t.Fatalf("get task: %d %s", r.Code, r.Body)
		}
		got = decode[taskResponse](t, r)
		if got.Status == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got.Status != "completed" {
		t.Fatalf("task never completed: %+v", got)
	}
	if got.Priority != "normal" {
		t.Errorf("priority = %q, want normal (the /tasks/submit default)", got.Priority)
	}

	// GET /tasks
	list := decode[listTasksResponse](t, do(t, s, "GET", "/tasks/", nil))
	if len(list.Tasks) != 1 {
		t.Errorf("list returned %d tasks", len(list.Tasks))
	}

	// Filters.
	if l := decode[listTasksResponse](t, do(t, s, "GET", "/tasks/?service=svc", nil)); len(l.Tasks) != 1 {
		t.Errorf("service filter returned %d", len(l.Tasks))
	}
	if l := decode[listTasksResponse](t, do(t, s, "GET", "/tasks/?status=queued", nil)); len(l.Tasks) != 0 {
		t.Errorf("status filter returned %d, want 0", len(l.Tasks))
	}
	// An unknown status is ignored rather than rejected.
	if l := decode[listTasksResponse](t, do(t, s, "GET", "/tasks/?status=bogus", nil)); len(l.Tasks) != 1 {
		t.Errorf("unknown status filter returned %d, want all", len(l.Tasks))
	}

	// Cancelling a finished task is a 404.
	if r := do(t, s, "POST", "/tasks/"+id+"/cancel", nil); r.Code != http.StatusNotFound {
		t.Errorf("cancel completed task = %d, want 404", r.Code)
	}
	if r := do(t, s, "GET", "/tasks/does-not-exist", nil); r.Code != http.StatusNotFound {
		t.Errorf("unknown task = %d, want 404", r.Code)
	}
}

func TestCancelQueuedTaskViaAPI(t *testing.T) {
	s, b := newTestServer(t)

	// A service that never becomes ready keeps the task queued.
	b.actions.Register(action.Definition{ID: "tag", Service: "blocked", Label: "tag"}, okHandler)
	b.services.Register(&service.Static{
		ServiceName: "blocked",
		ReadyFunc:   func(context.Context) bool { return false },
	})

	rec := do(t, s, "POST", "/tasks/submit", map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes"},
	})
	id := decode[submitTaskResponse](t, rec).TaskID

	if r := do(t, s, "POST", "/tasks/"+id+"/cancel", nil); r.Code != http.StatusOK {
		t.Fatalf("cancel: %d %s", r.Code, r.Body)
	}
	got := decode[taskResponse](t, do(t, s, "GET", "/tasks/"+id, nil))
	if got.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", got.Status)
	}
}

// The history route must win over /{task_id}.
func TestTaskHistoryRouteIsNotShadowed(t *testing.T) {
	s, b := newTestServer(t)

	if err := b.db.InsertTaskHistory(context.Background(), store.TaskHistoryEntry{
		TaskID: "t1", ActionID: "a", Service: "svc", Status: "completed", SubmittedAt: 1,
	}); err != nil {
		t.Fatalf("insert history: %v", err)
	}

	rec := do(t, s, "GET", "/tasks/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
	got := decode[map[string][]store.TaskHistoryEntry](t, rec)
	if len(got["history"]) != 1 || got["history"][0].TaskID != "t1" {
		t.Errorf("history = %v", got)
	}
}

func TestVersionAndHealth(t *testing.T) {
	s, _ := newTestServer(t)

	v := decode[map[string]any](t, do(t, s, "GET", "/version", nil))
	// The key name is db_alembic_head even though there is no alembic any more:
	// the frontend reads it.
	for _, key := range []string{"backend_version", "frontend_min_version", "db_alembic_head"} {
		if _, ok := v[key]; !ok {
			t.Errorf("version missing %q", key)
		}
	}
	if v["backend_version"] != "0.9.3" {
		t.Errorf("backend_version = %v; plugins declare required_backend against this", v["backend_version"])
	}

	if rec := do(t, s, "GET", "/system/health", nil); rec.Code != http.StatusOK {
		t.Errorf("health: %d", rec.Code)
	}
}

// With the subsystem disabled every route answers 503 except /version.
func TestDisabledBackendReturns503(t *testing.T) {
	s, b := newTestServer(t)
	b.ready = false

	rec := do(t, s, "GET", "/tasks/", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	wrapper := decode[map[string]json.RawMessage](t, rec)
	var detail map[string]any
	if err := json.Unmarshal(wrapper["detail"], &detail); err != nil {
		t.Fatalf("detail not an object: %s", rec.Body)
	}
	if detail["code"] != "AI_UNAVAILABLE" {
		t.Errorf("code = %v", detail["code"])
	}

	// The frontend polls version to discover whether a backend exists, and
	// renders health to explain what is wrong. Both must answer while off.
	if r := do(t, s, "GET", "/version", nil); r.Code != http.StatusOK {
		t.Errorf("version while disabled = %d, want 200", r.Code)
	}
	if r := do(t, s, "GET", "/system/health", nil); r.Code != http.StatusOK {
		t.Errorf("health while disabled = %d, want 200 so the UI can explain itself", r.Code)
	}
}

func TestMalformedBodyIs422(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest("POST", "/actions/available", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	s.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422 (FastAPI's validation status)", rec.Code)
	}
}

// ------------------------------------------------------------- websocket ---

func TestWebSocketStreamsTaskEvents(t *testing.T) {
	s, b := newTestServer(t)
	registerAction(b, "tag", "svc", nil, okHandler)

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()

	// The frontend appends ?api_key=; it must be accepted and ignored.
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/tasks?api_key=legacy"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if rec := do(t, s, "POST", "/tasks/submit", map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes"},
	}); rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body)
	}

	seen := map[string]bool{}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for len(seen) < 3 {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read (saw %v): %v", seen, err)
		}
		var msg struct {
			Type string         `json:"type"`
			Task map[string]any `json:"task"`
		}
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("decode %s: %v", raw, err)
		}
		seen[msg.Type] = true

		// Every message carries a full task summary in snake_case.
		for _, key := range []string{"id", "action_id", "status", "submitted_at", "cancel_requested"} {
			if _, ok := msg.Task[key]; !ok {
				t.Errorf("%s summary missing %q", msg.Type, key)
			}
		}
	}

	for _, want := range []string{"task.queued", "task.started", "task.completed"} {
		if !seen[want] {
			t.Errorf("missing %q (saw %v)", want, seen)
		}
	}
}

// A reconnecting dashboard should be populated immediately rather than waiting
// for the next transition.
func TestWebSocketSendsSnapshotOnConnect(t *testing.T) {
	s, b := newTestServer(t)

	b.actions.Register(action.Definition{ID: "tag", Service: "blocked", Label: "tag"}, okHandler)
	b.services.Register(&service.Static{
		ServiceName: "blocked",
		ReadyFunc:   func(context.Context) bool { return false },
	})

	rec := do(t, s, "POST", "/tasks/submit", map[string]any{
		"action_id": "tag",
		"context":   map[string]any{"page": "scenes"},
	})
	id := decode[submitTaskResponse](t, rec).TaskID

	srv := httptest.NewServer(s.Routes())
	defer srv.Close()

	conn, _, err := websocket.DefaultDialer.Dial(
		"ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/tasks", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var msg struct {
		Type string         `json:"type"`
		Task map[string]any `json:"task"`
	}
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if msg.Type != "task.snapshot" {
		t.Errorf("first message = %q, want task.snapshot", msg.Type)
	}
	if msg.Task["id"] != id {
		t.Errorf("snapshot id = %v, want %v", msg.Task["id"], id)
	}
}
