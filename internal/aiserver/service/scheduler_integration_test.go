package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/internal/aiserver/task"
)

// These check the property that matters at the system level: an unhealthy
// service must degrade only itself. A hanging or dead inference server is the
// normal failure mode, and it must not be able to stall the whole scheduler.

func noop(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
	return "ok", nil
}

func def(id, svc string) action.Definition {
	return action.Definition{ID: id, Service: svc, Label: id}
}

func newScheduler(t *testing.T, reg *service.Registry) *task.Manager {
	t.Helper()
	m := task.NewManager(task.Options{
		LoopInterval: 5 * time.Millisecond,
		Gate:         reg,
		// The scheduler's own probe bound, deliberately short: a slow service
		// is treated as not-ready rather than waited on.
		ReadyTimeout: 100 * time.Millisecond,
	})
	m.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m.Shutdown(ctx)
	})
	return m
}

// A service whose readiness probe never returns must not prevent other
// services from running.
func TestHangingServiceDoesNotStallOtherServices(t *testing.T) {
	release := make(chan struct{})
	hanging := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-release
	}))
	defer func() {
		close(release)
		hanging.Close()
	}()

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer healthy.Close()

	reg := service.NewRegistry()
	reg.Register(&service.RemoteService{ServiceName: "hanging", ServerURL: hanging.URL})
	reg.Register(&service.RemoteService{ServiceName: "healthy", ServerURL: healthy.URL})

	m := newScheduler(t, reg)

	var hangingRan atomic.Bool
	if _, err := m.Submit(def("a", "hanging"), func(context.Context, action.ContextInput, map[string]any, action.Handle) (any, error) {
		hangingRan.Store(true)
		return nil, nil
	}, action.ContextInput{Page: "scenes"}, nil, task.PriorityNormal, task.SubmitOptions{}); err != nil {
		t.Fatalf("submit to hanging service: %v", err)
	}

	rec, err := m.Submit(def("b", "healthy"), noop,
		action.ContextInput{Page: "scenes"}, nil, task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("submit to healthy service: %v", err)
	}

	// The healthy service must complete despite the other one hanging.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := m.Get(rec.ID)
		if err == nil && got.Status == task.StatusCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, err := m.Get(rec.ID)
	if err != nil || got.Status != task.StatusCompleted {
		t.Fatalf("healthy service task did not complete (status %q); a hanging peer stalled the scheduler", got.Status)
	}

	if hangingRan.Load() {
		t.Error("a task ran against a service whose readiness probe never returned")
	}
}

// A dead service parks its own queue and recovers when the server comes back,
// exercising the whole readiness cycle through the scheduler.
func TestServiceRecoveryResumesItsQueue(t *testing.T) {
	var up atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if up.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// Short windows so the test does not have to wait out the real 10s/20s.
	remote := &service.RemoteService{
		ServiceName:    "flaky",
		ServerURL:      srv.URL,
		ReadinessCache: 20 * time.Millisecond,
		FailureBackoff: 20 * time.Millisecond,
	}
	reg := service.NewRegistry()
	reg.Register(remote)

	m := newScheduler(t, reg)

	rec, err := m.Submit(def("a", "flaky"), noop,
		action.ContextInput{Page: "scenes"}, nil, task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	// While the server is down the task stays queued.
	time.Sleep(150 * time.Millisecond)
	got, err := m.Get(rec.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != task.StatusQueued {
		t.Fatalf("status = %q while the service was down, want queued", got.Status)
	}
	if state := remote.ConnectivityDetails().State; state != service.StateUnreachable && state != service.StateWaiting {
		t.Errorf("state = %q, want unreachable or waiting", state)
	}

	// Bring it back; the queue must drain without any external nudge.
	up.Store(true)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err = m.Get(rec.ID)
		if err == nil && got.Status == task.StatusCompleted {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if got.Status != task.StatusCompleted {
		t.Fatalf("task did not resume after recovery (status %q)", got.Status)
	}
	if state := remote.ConnectivityDetails().State; state != service.StateReady {
		t.Errorf("state after recovery = %q, want ready", state)
	}
}

// A local service - no server URL - runs immediately with no network at all.
func TestLocalServiceRunsWithoutProbing(t *testing.T) {
	reg := service.NewRegistry()
	reg.Register(&service.RemoteService{ServiceName: "native", Concurrency: 2})

	m := newScheduler(t, reg)

	rec, err := m.Submit(def("a", "native"), noop,
		action.ContextInput{Page: "scenes"}, nil, task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, err := m.Get(rec.ID)
		if err == nil && got.Status == task.StatusCompleted {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("a local service task never completed")
}
