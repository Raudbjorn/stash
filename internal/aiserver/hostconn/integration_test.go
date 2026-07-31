package hostconn_test

// This is the test the whole of workstream B is aimed at: the real embedded
// Python runtime, spawned by the real supervisor, reached by the real D-Bus
// client. The unit tests either side of it mock one half or the other; only
// this one proves the two halves agree.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/host"
	"github.com/stashapp/stash/internal/aiserver/hostconn"
	"github.com/stashapp/stash/internal/aiserver/proc"
	"github.com/stashapp/stash/internal/aiserver/pyenv"
	"github.com/stashapp/stash/internal/aiserver/pyhost"
)

// requirePyGObject skips when the machine cannot host plugins, which is the
// same condition the supervisor treats as Unavailable rather than an error.
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

// shortDir keeps unix socket paths inside the kernel's ~108-byte cap, which a
// t.TempDir() under a long test name overflows.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "aih*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// newSupervisor wires the real runtime to the real transport.
func newSupervisor(t *testing.T, python string, opts hostconn.Options) (*host.Supervisor, string) {
	t.Helper()

	base := shortDir(t)
	runtimeDir := filepath.Join(base, "runtime")
	if _, err := pyhost.Extract(runtimeDir); err != nil {
		t.Fatalf("extract runtime: %v", err)
	}

	sup := host.New(host.Config{
		Enabled:   true,
		BaseDir:   base,
		PluginDir: filepath.Join(base, "plugins"),
		LogLevel:  "debug",
		// The system interpreter stands in for the venv: creating one per test
		// would add tens of seconds and prove nothing about the transport.
		PrepareEnv: func(context.Context) (pyenv.Env, error) {
			return pyenv.Env{
				Dir:         base,
				Interpreter: python,
				Report: pyenv.Report{
					Status:      pyenv.StatusOK,
					Message:     "test interpreter",
					Interpreter: python,
				},
			}, nil
		},
		Connect: func(ctx context.Context, address, token string) (host.Conn, error) {
			return hostconn.Dial(ctx, address, token, opts)
		},
	})

	sup.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		sup.Stop(ctx)
	})

	return sup, base
}

func waitReady(t *testing.T, sup *host.Supervisor) {
	t.Helper()

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		status := sup.Status()
		switch status.State {
		case proc.StateReady:
			return
		case proc.StateFailed, proc.StateUnavailable:
			t.Fatalf("host reached %s: %s (%s)", status.State, status.Message, status.Remediation)
		}
		time.Sleep(50 * time.Millisecond)
	}

	status := sup.Status()
	t.Fatalf("host never became ready: state %s, %s", status.State, status.Message)
}

// The headline: the embedded runtime starts, binds, and completes the
// authenticated handshake against the Go client.
func TestRealHostReachesReady(t *testing.T) {
	python := requirePyGObject(t)
	sup, _ := newSupervisor(t, python, hostconn.Options{})
	waitReady(t, sup)

	status := sup.Status()
	if status.PID == 0 {
		t.Error("ready host reported no PID")
	}
	if status.LastCrash != nil {
		t.Errorf("unexpected crash: %+v", status.LastCrash)
	}
}

// Loading a plugin is the one thing that genuinely has to happen in Python.
// Everything about this plugin - its manifest, its directory - was decided in
// Go; the host only imports it and reports what registered.
func TestLoadPluginRegistersAnAction(t *testing.T) {
	python := requirePyGObject(t)

	var (
		mu       sync.Mutex
		progress []string
	)
	opts := hostconn.Options{
		Bridge: &recordingBridge{settings: map[string]any{"threshold": 0.5}},
		OnProgress: func(id string, fraction float64, message string, _ map[string]any) {
			mu.Lock()
			progress = append(progress, id)
			mu.Unlock()
		},
	}

	sup, base := newSupervisor(t, python, opts)
	waitReady(t, sup)

	pluginDir := filepath.Join(base, "plugins", "demo")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `
from stash_ai.actions import action

@action(id="demo.echo", label="Echo", contexts=["scene"])
def echo(ctx, params):
    return {"echoed": params.get("value")}
`
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.py"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	conn := supervisorConn(t, sup, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := conn.Load(ctx, "demo", pluginDir, map[string]any{
		"name":  "demo",
		"files": []string{"plugin"},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("Load status = %q: %s", result.Status, result.Error)
	}

	if len(result.Registry.Actions) != 1 {
		t.Fatalf("registered %d actions, want 1: %+v", len(result.Registry.Actions), result.Registry.Actions)
	}
	if got := result.Registry.Actions[0]["id"]; got != "demo.echo" {
		t.Errorf("action id = %v, want demo.echo", got)
	}

	// The action runs in the host and its result crosses back as JSON.
	out, err := conn.InvokeAction(ctx, "inv-1", "demo.echo",
		map[string]any{"entityId": "7"}, map[string]any{"value": "hello"})
	if err != nil {
		t.Fatalf("InvokeAction: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want an object", out)
	}
	if payload["echoed"] != "hello" {
		t.Errorf("echoed = %v, want hello", payload["echoed"])
	}

	// Unloading drops the registration, which is what makes reload possible
	// without recycling the process.
	if err := conn.Unload(ctx, "demo"); err != nil {
		t.Fatalf("Unload: %v", err)
	}
	registry, err := conn.ListPlugins(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(registry.Actions) != 0 {
		t.Errorf("after Unload the registry still holds %+v", registry.Actions)
	}
}

// A plugin that fails to import must be reported, not raised: one broken plugin
// cannot be allowed to stop the others loading.
func TestLoadReportsAFailingPlugin(t *testing.T) {
	python := requirePyGObject(t)
	sup, base := newSupervisor(t, python, hostconn.Options{})
	waitReady(t, sup)

	pluginDir := filepath.Join(base, "plugins", "broken")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.py"),
		[]byte("raise RuntimeError('deliberate failure')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	conn := supervisorConn(t, sup, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := conn.Load(ctx, "broken", pluginDir, map[string]any{"name": "broken"})
	if err != nil {
		t.Fatalf("Load returned a transport error rather than a status: %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("status = %q, want error", result.Status)
	}
	if !strings.Contains(result.Error, "deliberate failure") {
		t.Errorf("error does not name the cause: %s", result.Error)
	}

	// The host must still be usable afterwards.
	if err := conn.Ping(ctx); err != nil {
		t.Errorf("host unusable after a failed load: %v", err)
	}
	if sup.Status().State != proc.StateReady {
		t.Errorf("supervisor left %s after a failed load", sup.Status().State)
	}
}

// The token is the only thing standing between a local process and the Bridge,
// so a wrong one must be refused.
func TestHandshakeRejectsABadToken(t *testing.T) {
	python := requirePyGObject(t)
	sup, _ := newSupervisor(t, python, hostconn.Options{})
	waitReady(t, sup)

	address := sup.Address()
	if address == "" {
		t.Fatal("supervisor reported no address")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := hostconn.Dial(ctx, address, "not-the-token", hostconn.Options{}); err == nil {
		t.Fatal("Dial accepted a bad token")
	}
}

// GetInfo is what the settings page shows when the host misbehaves, so it has
// to report the process actually running rather than the build's assumptions.
func TestInfoDescribesTheRunningHost(t *testing.T) {
	python := requirePyGObject(t)
	sup, _ := newSupervisor(t, python, hostconn.Options{})
	waitReady(t, sup)

	conn := supervisorConn(t, sup, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := conn.Info(ctx)
	if err != nil {
		t.Fatalf("Info: %v", err)
	}
	for _, key := range []string{"python", "pid", "gi", "plugins_loaded"} {
		if _, ok := info[key]; !ok {
			t.Errorf("GetInfo is missing %q: %v", key, info)
		}
	}
	if pid, ok := info["pid"].(float64); !ok || int(pid) != sup.Status().PID {
		t.Errorf("GetInfo pid = %v, supervisor says %d", info["pid"], sup.Status().PID)
	}
}

// supervisorConn opens a second client connection to the running host.
//
// The supervisor's own connection is private to it; tests dial again rather
// than reaching into it, which also proves the host serves more than one peer.
//
// The bridge is explicit because the host calls back on its most recent
// authenticated peer - correct in production, where Stash is the only peer, but
// it means a test asserting on bridge calls must pass the bridge it will check.
func supervisorConn(t *testing.T, sup *host.Supervisor, bridge hostconn.Bridge) *hostconn.Conn {
	t.Helper()

	address, token := sup.Address(), sup.Token()
	if address == "" || token == "" {
		t.Fatal("supervisor has no live host")
	}
	if bridge == nil {
		bridge = &recordingBridge{}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := hostconn.Dial(ctx, address, token, hostconn.Options{Bridge: bridge})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// recordingBridge is a Bridge that answers plausibly and remembers what it was
// asked, so tests can assert on the callback direction.
type recordingBridge struct {
	mu       sync.Mutex
	settings map[string]any
	queries  []string
	tasks    []string
	batches  [][]hostconn.BatchStatement
	markedID string

	// rejectForeignTables mimics the store's namespace rule, so the sandbox can
	// be tested from inside a plugin without standing up a database.
	rejectForeignTables bool
}

func (b *recordingBridge) Query(_ context.Context, plugin, sql string, _ []any) ([]map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queries = append(b.queries, sql)

	if b.rejectForeignTables && !strings.Contains(sql, "p_"+plugin+"_") {
		return nil, fmt.Errorf("plugin %s may not access that table; its tables live in the p_%s_ namespace", plugin, plugin)
	}
	return []map[string]any{{"ok": 1}}, nil
}

func (b *recordingBridge) ExecuteBatch(_ context.Context, _ string, statements []hostconn.BatchStatement) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.batches = append(b.batches, statements)
	return int64(len(statements)), nil
}

func (b *recordingBridge) MarkController(_ context.Context, _, invocationID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.markedID = invocationID
	return nil
}

func (b *recordingBridge) batchCalls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.batches)
}

func (b *recordingBridge) batchSize() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.batches) == 0 {
		return 0
	}
	return len(b.batches[0])
}

func (b *recordingBridge) markedController() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.markedID != ""
}

func (b *recordingBridge) Execute(_ context.Context, _, sql string, _ []any) (int64, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queries = append(b.queries, sql)
	return 1, nil
}

func (b *recordingBridge) GetSettings(context.Context, string) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.settings == nil {
		return map[string]any{}, nil
	}
	return b.settings, nil
}

func (b *recordingBridge) SetSetting(_ context.Context, _, key string, value any) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.settings == nil {
		b.settings = map[string]any{}
	}
	b.settings[key] = value
	return nil
}

func (b *recordingBridge) StashGraphQL(_ context.Context, _, query string, _ map[string]any) (map[string]any, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.queries = append(b.queries, query)
	return map[string]any{"data": map[string]any{}}, nil
}

func (b *recordingBridge) SubmitTask(_ context.Context, _, actionID string, _, _ map[string]any, _, _ string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tasks = append(b.tasks, actionID)
	return "task-" + actionID, nil
}

func (b *recordingBridge) taskIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.tasks...)
}
