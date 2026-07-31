package pluginhost_test

// End-to-end plugin dispatch: a task submitted to the Go scheduler whose body
// runs in the real Python host. Everything below the test is production code -
// the supervisor, the transport, the embedded runtime - because the failures
// worth catching here are the ones that only appear when all of it is real.

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/action"
	"github.com/stashapp/stash/internal/aiserver/host"
	"github.com/stashapp/stash/internal/aiserver/hostconn"
	"github.com/stashapp/stash/internal/aiserver/pluginhost"
	"github.com/stashapp/stash/internal/aiserver/proc"
	"github.com/stashapp/stash/internal/aiserver/pyenv"
	"github.com/stashapp/stash/internal/aiserver/pyhost"
	"github.com/stashapp/stash/internal/aiserver/recommend"
	"github.com/stashapp/stash/internal/aiserver/service"
	"github.com/stashapp/stash/internal/aiserver/store"
	"github.com/stashapp/stash/internal/aiserver/task"
)

type harness struct {
	sup      *host.Supervisor
	plugins  *pluginhost.Manager
	tasks    *task.Manager
	actions  *action.Registry
	services *service.Registry
	recs     *recommend.Registry
	db       *store.DB
	base     string
}

func requirePyGObject(t *testing.T) string {
	t.Helper()

	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	probe := exec.Command(python, "-c",
		"import gi; gi.require_version('Gio','2.0'); from gi.repository import Gio, GLib")
	if out, err := probe.CombinedOutput(); err != nil {
		t.Skipf("PyGObject not available: %v: %s", err, out)
	}
	return python
}

// newHarness stands up the whole stack against a real Python host.
func newHarness(t *testing.T, plugins map[string]string) *harness {
	t.Helper()
	python := requirePyGObject(t)

	// Short path: unix sockets cap near 108 bytes and t.TempDir() under a long
	// test name overflows that.
	base, err := os.MkdirTemp("", "aiph*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })

	if _, err := pyhost.Extract(filepath.Join(base, "runtime")); err != nil {
		t.Fatalf("extract runtime: %v", err)
	}

	pluginDir := filepath.Join(base, "plugins")
	for name, source := range plugins {
		dir := filepath.Join(pluginDir, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "plugin.py"), []byte(source), 0o644); err != nil {
			t.Fatal(err)
		}
		manifest := "name: " + name + "\nversion: 1.0.0\nfiles:\n  - plugin\n"
		if err := os.WriteFile(filepath.Join(dir, "plugin.yml"), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	db, err := store.Open(context.Background(), filepath.Join(base, "ai.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	h := &harness{
		actions:  action.NewRegistry(),
		services: service.NewRegistry(),
		recs:     recommend.NewRegistry(),
		db:       db,
		base:     base,
	}

	h.tasks = task.NewManager(task.Options{Gate: h.services})
	h.tasks.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		h.tasks.Shutdown(ctx)
	})

	h.plugins = pluginhost.New(pluginhost.Deps{
		DB:        func() *store.DB { return h.db },
		Tasks:     func() *task.Manager { return h.tasks },
		Actions:   h.actions,
		Services:  h.services,
		Recs:      h.recs,
		GraphQL:   func() http.Handler { return nil },
		PluginDir: pluginDir,
	}, nil)

	loaded := make(chan struct{}, 4)
	h.sup = host.New(host.Config{
		Enabled:   true,
		BaseDir:   base,
		PluginDir: pluginDir,
		LogLevel:  "debug",
		PrepareEnv: func(context.Context) (pyenv.Env, error) {
			return pyenv.Env{
				Dir:         base,
				Interpreter: python,
				Report:      pyenv.Report{Status: pyenv.StatusOK, Interpreter: python},
			}, nil
		},
		Connect: func(ctx context.Context, address, token string) (host.Conn, error) {
			return hostconn.Dial(ctx, address, token, hostconn.Options{
				Bridge:     h.plugins.Bridge(),
				OnProgress: h.plugins.OnProgress(),
			})
		},
		OnReady: func(int) {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				defer cancel()
				if err := h.plugins.SyncFromHost(ctx); err != nil {
					t.Logf("sync: %v", err)
				}
				select {
				case loaded <- struct{}{}:
				default:
				}
			}()
		},
		OnGenerationEnd: h.plugins.HostCrashed,
	})
	h.plugins.SetSupervisor(h.sup)
	h.sup.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		h.sup.Stop(ctx)
	})

	select {
	case <-loaded:
	case <-time.After(60 * time.Second):
		t.Fatalf("plugins never loaded; host state %s", h.sup.Status().State)
	}
	return h
}

// waitStatus polls a task until it reaches a terminal state.
func (h *harness) waitStatus(t *testing.T, id string, within time.Duration) task.Record {
	t.Helper()

	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		rec, err := h.tasks.Get(id)
		if err == nil && rec.Status.Terminal() {
			return rec
		}
		time.Sleep(25 * time.Millisecond)
	}

	rec, _ := h.tasks.Get(id)
	t.Fatalf("task %s never finished; status %q", id, rec.Status)
	return task.Record{}
}

const echoPlugin = `
from stash_ai.actions import action
from stash_ai import services, tasks

services.register_service("demo", max_concurrency=2)


@action(id="demo.echo", label="Echo", service="demo")
def echo(ctx, params):
    return {"echoed": params.get("value"), "entity": ctx.get("entityId")}


@action(id="demo.slow", label="Slow", service="demo")
def slow(ctx, params):
    import time
    for i in range(400):
        tasks.progress(i / 400.0, "working")
        if tasks.cancelled():
            return {"cancelled": True}
        time.sleep(0.05)
    return {"cancelled": False}
`

// A plugin's action becomes an ordinary scheduled task: the scheduler does not
// know or care that its body runs in another process.
func TestPluginActionRunsThroughTheScheduler(t *testing.T) {
	h := newHarness(t, map[string]string{"demo": echoPlugin})

	entity := "42"
	in := action.ContextInput{Page: "scenes", EntityID: &entity, IsDetailView: true}

	registration, ok := h.actions.Resolve("demo.echo", in)
	if !ok {
		t.Fatalf("demo.echo was not published; registry holds %v", h.actions.IDs())
	}
	if registration.Definition.Service != "demo" {
		t.Errorf("service = %q, want demo", registration.Definition.Service)
	}

	// The declared service must reach the scheduler's gate too, or its
	// concurrency limit is silently ignored.
	svc, ok := h.services.Get("demo")
	if !ok {
		t.Fatal("the plugin's service was not registered")
	}
	if svc.MaxConcurrency() != 2 {
		t.Errorf("max concurrency = %d, want 2", svc.MaxConcurrency())
	}

	rec, err := h.tasks.Submit(registration.Definition, registration.Handler, in,
		map[string]any{"value": "hello"}, task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	final := h.waitStatus(t, rec.ID, 30*time.Second)
	if final.Status != task.StatusCompleted {
		t.Fatalf("status = %q, error %q", final.Status, final.Error)
	}

	result, ok := final.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", final.Result)
	}
	if result["echoed"] != "hello" {
		t.Errorf("echoed = %v, want hello", result["echoed"])
	}
	// The context must arrive in camelCase, exactly as the frontend sends it -
	// this is what lets existing plugins run unchanged.
	if result["entity"] != "42" {
		t.Errorf("entityId = %v, want \"42\"", result["entity"])
	}
}

// Progress from a plugin has to reach the scheduler's event bus, since that is
// what the WebSocket publishes and the progress ring reads.
func TestPluginProgressReachesTheEventBus(t *testing.T) {
	h := newHarness(t, map[string]string{"demo": echoPlugin})

	sub := h.tasks.Subscribe(64)
	defer sub.Close()

	in := action.ContextInput{Page: "scenes"}
	registration, ok := h.actions.Resolve("demo.slow", in)
	if !ok {
		t.Fatal("demo.slow was not published")
	}

	rec, err := h.tasks.Submit(registration.Definition, registration.Handler, in, nil,
		task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	deadline := time.After(20 * time.Second)
	for {
		select {
		case event := <-sub.Events:
			if event.Type != task.EventProgress || event.Task.ID != rec.ID {
				continue
			}
			if _, ok := event.Extra["progress"]; !ok {
				t.Errorf("progress event carries no progress value: %v", event.Extra)
			}
			h.tasks.Cancel(rec.ID)
			h.waitStatus(t, rec.ID, 30*time.Second)
			return
		case <-deadline:
			t.Fatal("no progress event reached the task event bus")
		}
	}
}

// Cancelling a task must reach the plugin, which is only possible because
// Cancel travels as its own call rather than queueing behind the invocation.
func TestCancelPropagatesToThePlugin(t *testing.T) {
	h := newHarness(t, map[string]string{"demo": echoPlugin})

	in := action.ContextInput{Page: "scenes"}
	registration, _ := h.actions.Resolve("demo.slow", in)

	rec, err := h.tasks.Submit(registration.Definition, registration.Handler, in, nil,
		task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Let it actually start; cancelling a queued task takes a different path.
	waitRunning(t, h, rec.ID)

	h.tasks.Cancel(rec.ID)

	final := h.waitStatus(t, rec.ID, 30*time.Second)
	if final.Status != task.StatusCancelled {
		t.Errorf("status = %q, want cancelled (error %q)", final.Status, final.Error)
	}
}

// The failure this guards against is the worst kind: the host dies and the task
// stays "running" forever, so the UI's progress ring spins with nothing behind
// it. It must end terminally instead.
func TestHostCrashFailsInFlightTasks(t *testing.T) {
	h := newHarness(t, map[string]string{"demo": echoPlugin})

	in := action.ContextInput{Page: "scenes"}
	registration, _ := h.actions.Resolve("demo.slow", in)

	rec, err := h.tasks.Submit(registration.Definition, registration.Handler, in, nil,
		task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	waitRunning(t, h, rec.ID)

	if h.plugins.InFlight() != 1 {
		t.Fatalf("in flight = %d, want 1", h.plugins.InFlight())
	}

	pid := h.sup.Status().PID
	if pid == 0 {
		t.Fatal("no host process to kill")
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill host: %v", err)
	}

	final := h.waitStatus(t, rec.ID, 45*time.Second)
	if final.Status == task.StatusCompleted {
		t.Fatal("a task survived the death of the process running it")
	}
	if final.Error == "" {
		t.Error("the task failed with no explanation")
	}

	// And the supervisor must bring a replacement up rather than giving up.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if h.sup.Status().State == proc.StateReady {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("the host did not recover; state %s", h.sup.Status().State)
}

// A plugin whose registrations are withdrawn must stop being offered, or the UI
// shows a button that can only fail.
func TestUnloadWithdrawsRegistrations(t *testing.T) {
	h := newHarness(t, map[string]string{"demo": echoPlugin})

	if len(h.actions.IDs()) == 0 {
		t.Fatal("nothing was registered to withdraw")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := h.plugins.Unload(ctx, "demo"); err != nil {
		t.Fatalf("Unload: %v", err)
	}

	if ids := h.actions.IDs(); len(ids) != 0 {
		t.Errorf("actions still registered after unload: %v", ids)
	}
	if _, ok := h.services.Get("demo"); ok {
		t.Error("the service survived its plugin's unload")
	}
}

// A task submitted while the host is down must fail with a reason that blames
// the host rather than the plugin.
func TestSubmittingWithNoHostFailsClearly(t *testing.T) {
	h := newHarness(t, map[string]string{"demo": echoPlugin})

	in := action.ContextInput{Page: "scenes"}
	registration, _ := h.actions.Resolve("demo.echo", in)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	h.sup.Stop(ctx)
	cancel()

	rec, err := h.tasks.Submit(registration.Definition, registration.Handler, in, nil,
		task.PriorityNormal, task.SubmitOptions{})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	final := h.waitStatus(t, rec.ID, 30*time.Second)
	if final.Status != task.StatusFailed {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	if !errorMentionsHost(final.Error) {
		t.Errorf("error %q does not explain that the host is down", final.Error)
	}
}

func errorMentionsHost(message string) bool {
	return strings.Contains(message, "plugin host")
}

func waitRunning(t *testing.T, h *harness, id string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if rec, err := h.tasks.Get(id); err == nil && rec.Status == task.StatusRunning {
			// Give the invocation a moment to actually reach the host.
			time.Sleep(200 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("task %s never started running", id)
}
