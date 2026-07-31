package aiserver

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/tagging"
	"github.com/stashapp/stash/internal/aiserver/task"
	"github.com/stashapp/stash/pkg/aitag"
	"github.com/stashapp/stash/pkg/models"
)

type closeOrderProvider struct{ closed atomic.Bool }

func (*closeOrderProvider) Name() string                                      { return "order-test" }
func (*closeOrderProvider) Capabilities() aitag.Capability                    { return 0 }
func (*closeOrderProvider) Available(context.Context) error                   { return nil }
func (*closeOrderProvider) Models(context.Context) ([]aitag.ModelInfo, error) { return nil, nil }
func (*closeOrderProvider) AnalyzeVideo(context.Context, string, aitag.Options, aitag.Sink) (*aitag.Result, error) {
	return nil, nil
}
func (*closeOrderProvider) AnalyzeImages(context.Context, []string, aitag.Options) (*aitag.ImageResult, error) {
	return nil, nil
}
func (p *closeOrderProvider) Close() error { p.closed.Store(true); return nil }

func TestShutdownDrainsTasksBeforeClosingTaggingProvider(t *testing.T) {
	server := New(Deps{})
	defer server.api.Close()
	provider := &closeOrderProvider{}
	tagger := tagging.NewService(models.Repository{}, nil, tagging.Config{Provider: provider})
	tagger.Register(server.actions, server.services)
	manager := task.NewManager(task.Options{Gate: server.services})
	manager.Start()
	server.mu.Lock()
	server.tagging = tagger
	server.tasks = manager
	server.state = StateReady
	server.mu.Unlock()

	started := make(chan struct{})
	returned := make(chan struct{})
	closedTooEarly := atomic.Bool{}
	definition := action.Definition{ID: "shutdown-order", Service: tagging.ServiceName}
	_, err := manager.Submit(definition, func(ctx context.Context, _ action.ContextInput, _ map[string]any, _ action.Handle) (any, error) {
		close(started)
		<-ctx.Done()
		closedTooEarly.Store(provider.closed.Load())
		close(returned)
		return nil, ctx.Err()
	}, action.ContextInput{Page: "scenes"}, nil, task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("task did not start")
	}

	server.Shutdown()
	select {
	case <-returned:
	default:
		t.Fatal("Shutdown returned before the handler")
	}
	if closedTooEarly.Load() {
		t.Error("the provider closed while its handler was still running")
	}
	if !provider.closed.Load() {
		t.Error("the provider was not closed after handlers drained")
	}
	if len(server.actions.Get(tagging.ActionID)) != 0 {
		t.Error("tagging actions remained registered during shutdown")
	}
	if _, ok := server.services.Get(tagging.ServiceName); ok {
		t.Error("tagging service remained registered during shutdown")
	}
}
