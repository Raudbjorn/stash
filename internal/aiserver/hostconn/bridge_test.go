package hostconn_test

// Bridge round trips: a plugin calling back into Stash. These run against the
// real embedded runtime, so they test the SDK, the marshalling and the Go
// export together - the places a hand-written IPC contract actually breaks.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/hostconn"
)

// loadPlugin writes a single-module plugin and loads it into the host.
func loadPlugin(t *testing.T, conn *hostconn.Conn, base, name, source string) hostconn.LoadResult {
	t.Helper()

	dir := filepath.Join(base, "plugins", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.py"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := conn.Load(ctx, name, dir, map[string]any{"name": name, "files": []string{"plugin"}})
	if err != nil {
		t.Fatalf("Load %s: %v", name, err)
	}
	if result.Status != "ok" {
		t.Fatalf("Load %s: %s", name, result.Error)
	}
	return result
}

// Every bridge method exercised from plugin code in one pass, because what
// breaks is usually one argument's marshalling rather than the mechanism.
func TestBridgeRoundTrips(t *testing.T) {
	python := requirePyGObject(t)

	recorder := &recordingBridge{settings: map[string]any{"threshold": 0.75, "label": "x"}}
	sup, base := newSupervisor(t, python, hostconn.Options{Bridge: recorder})
	waitReady(t, sup)

	conn := supervisorConn(t, sup, recorder)

	source := `
from stash_ai.actions import action
from stash_ai import db, settings, stash, tasks


@action(id="bridge.all", label="All")
def run(ctx, params):
    rows = db.query("SELECT id FROM p_bridge_items WHERE id = ?", [1])
    affected = db.execute("INSERT INTO p_bridge_items (id) VALUES (?)", [2])

    with db.batch() as b:
        b.execute("INSERT INTO p_bridge_items (id) VALUES (?)", [3])
        b.execute("INSERT INTO p_bridge_items (id) VALUES (?)", [4])

    current = settings.get_settings()
    settings.set_setting("threshold", 0.9)

    gql = stash.graphql("query { version { version } }", {"x": 1})

    child = tasks.submit_child_tasks("bridge.child", [7, 8], chunk_size=1)

    return {
        "rows": rows,
        "affected": affected,
        "settings": current,
        "gql": gql,
        "children": child,
    }
`
	loadPlugin(t, conn, base, "bridge", source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := conn.InvokeAction(ctx, "inv-bridge", "bridge.all", nil, nil)
	if err != nil {
		t.Fatalf("InvokeAction: %v", err)
	}

	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", out)
	}

	// Query: rows come back as a list of objects.
	rows, ok := payload["rows"].([]any)
	if !ok || len(rows) != 1 {
		t.Errorf("rows = %#v, want one row", payload["rows"])
	}

	// Execute: the affected count crosses back as a number.
	if affected, ok := payload["affected"].(float64); !ok || affected != 1 {
		t.Errorf("affected = %#v, want 1", payload["affected"])
	}

	// Settings: the plugin sees effective values, and its write is stored.
	stored, ok := payload["settings"].(map[string]any)
	if !ok || stored["threshold"] != 0.75 {
		t.Errorf("settings = %#v, want threshold 0.75", payload["settings"])
	}
	recorder.mu.Lock()
	written := recorder.settings["threshold"]
	recorder.mu.Unlock()
	if written != 0.9 {
		t.Errorf("set_setting stored %#v, want 0.9", written)
	}

	// GraphQL: the response shape survives the round trip.
	if _, ok := payload["gql"].(map[string]any); !ok {
		t.Errorf("gql = %#v, want an object", payload["gql"])
	}

	// Fan-out: one child per item, each with an id from Stash.
	children, ok := payload["children"].([]any)
	if !ok || len(children) != 2 {
		t.Fatalf("children = %#v, want 2", payload["children"])
	}
	if got := recorder.taskIDs(); len(got) != 2 {
		t.Errorf("submitted %d child tasks, want 2: %v", len(got), got)
	}

	// The batch must arrive as ONE call, not one per statement - that is the
	// entire reason batch() exists.
	if n := recorder.batchCalls(); n != 1 {
		t.Errorf("batch produced %d ExecuteBatch calls, want exactly 1", n)
	}
	if n := recorder.batchSize(); n != 2 {
		t.Errorf("batch carried %d statements, want 2", n)
	}

	// Fan-out must release the parent's slot, or a service with concurrency 1
	// deadlocks waiting on children it is itself blocking.
	if !recorder.markedController() {
		t.Error("submit_child_tasks did not mark the parent a controller")
	}
}

// Progress must reach Stash while the handler is still running - after it
// finishes it is worthless, and a buffered implementation would still pass a
// naive assertion.
func TestProgressArrivesDuringTheHandler(t *testing.T) {
	python := requirePyGObject(t)

	var (
		mu       sync.Mutex
		received []float64
		messages []string
	)
	seen := make(chan struct{}, 8)

	opts := hostconn.Options{
		Bridge: &recordingBridge{},
		OnProgress: func(id string, fraction float64, message string, detail map[string]any) {
			mu.Lock()
			received = append(received, fraction)
			messages = append(messages, message)
			mu.Unlock()
			select {
			case seen <- struct{}{}:
			default:
			}
		},
	}

	sup, base := newSupervisor(t, python, opts)
	waitReady(t, sup)
	conn := supervisorConn(t, sup, nil)

	source := `
import threading
from stash_ai.actions import action
from stash_ai import tasks

_release = threading.Event()


@action(id="progress.demo", label="Progress")
def run(ctx, params):
    tasks.progress(0.25, "quarter", stage="early")
    tasks.progress(0.5, "half")
    # Block until the test has observed progress, proving it arrived DURING
    # the handler rather than being flushed at the end.
    _release.wait(timeout=10)
    return {"done": True}


@action(id="progress.release", label="Release")
def release(ctx, params):
    _release.set()
    return {"released": True}
`
	loadPlugin(t, conn, base, "progress", source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result := make(chan error, 1)
	go func() {
		_, err := conn.InvokeAction(ctx, "inv-progress", "progress.demo", nil, nil)
		result <- err
	}()

	// Wait for progress while the handler is definitely still blocked.
	select {
	case <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("no progress signal arrived while the handler was running")
	}

	// Released through a second action, which also proves the host stays
	// responsive while an invocation is in flight.
	if _, err := conn.InvokeAction(ctx, "inv-release", "progress.release", nil, nil); err != nil {
		t.Fatalf("release: %v", err)
	}

	if err := <-result; err != nil {
		t.Fatalf("InvokeAction: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(received) < 2 {
		t.Fatalf("received %d progress signals, want at least 2: %v", len(received), received)
	}
	if received[0] != 0.25 || received[1] != 0.5 {
		t.Errorf("fractions = %v, want 0.25 then 0.5", received)
	}
	if messages[0] != "quarter" {
		t.Errorf("message = %q, want \"quarter\"", messages[0])
	}
}

// Cancel has to reach a plugin that is already running, which is the whole
// reason handlers run on worker threads rather than the GLib loop.
func TestCancelReachesARunningHandler(t *testing.T) {
	python := requirePyGObject(t)

	sup, base := newSupervisor(t, python, hostconn.Options{Bridge: &recordingBridge{}})
	waitReady(t, sup)
	conn := supervisorConn(t, sup, nil)

	source := `
import time
from stash_ai.actions import action
from stash_ai import tasks


@action(id="cancel.demo", label="Cancel")
def run(ctx, params):
    for _ in range(200):
        if tasks.cancelled():
            return {"stopped": True}
        time.sleep(0.05)
    return {"stopped": False}
`
	loadPlugin(t, conn, base, "cancel", source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	done := make(chan any, 1)
	go func() {
		out, _ := conn.InvokeAction(ctx, "inv-cancel", "cancel.demo", nil, nil)
		done <- out
	}()

	// Give the handler a moment to actually start; cancelling an invocation the
	// host has not begun would prove nothing.
	time.Sleep(300 * time.Millisecond)

	cancelCtx, cancelCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCancel()
	if err := conn.Cancel(cancelCtx, "inv-cancel"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case out := <-done:
		payload, ok := out.(map[string]any)
		if !ok {
			t.Fatalf("result = %#v, want an object", out)
		}
		if payload["stopped"] != true {
			t.Error("the handler ran to completion instead of noticing cancellation")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancellation never reached the handler")
	}
}

// The database sandbox has to hold from inside a plugin, not just in the Go
// unit test: this is the boundary that keeps one plugin out of another's data.
func TestPluginCannotEscapeItsNamespace(t *testing.T) {
	python := requirePyGObject(t)

	// A bridge that refuses foreign tables exactly as the real store does.
	guard := &recordingBridge{rejectForeignTables: true}
	sup, base := newSupervisor(t, python, hostconn.Options{Bridge: guard})
	waitReady(t, sup)
	conn := supervisorConn(t, sup, guard)

	source := `
from stash_ai.actions import action
from stash_ai import db


@action(id="escape.demo", label="Escape")
def run(ctx, params):
    try:
        db.query("SELECT * FROM settings")
    except Exception as exc:
        return {"refused": True, "why": str(exc)}
    return {"refused": False}
`
	loadPlugin(t, conn, base, "escape", source)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := conn.InvokeAction(ctx, "inv-escape", "escape.demo", nil, nil)
	if err != nil {
		t.Fatalf("InvokeAction: %v", err)
	}

	payload, _ := out.(map[string]any)
	if payload["refused"] != true {
		t.Fatalf("a plugin read a table outside its namespace: %#v", payload)
	}
	// The error must survive the trip intact, or the plugin author sees only
	// "call failed" and cannot act on it.
	if why, _ := payload["why"].(string); !strings.Contains(why, "namespace") {
		t.Errorf("the rejection reason did not reach the plugin: %q", why)
	}
}
