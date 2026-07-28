package llamahost

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/pkg/onnx"
	"github.com/stashapp/stash/internal/aiserver/proc"
)

func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_LLAMA_HELPER") == "1" {
		runLlamaHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runLlamaHelper() {
	value := func(flag string) string {
		for i, arg := range os.Args {
			if arg == flag && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
		}
		return ""
	}
	socket := value("--host")
	if argsFile := os.Getenv("LLAMA_ARGS_FILE"); argsFile != "" {
		_ = os.WriteFile(argsFile, []byte(strings.Join(os.Args[1:], "\x00")), 0o600)
	}
	if envFile := os.Getenv("LLAMA_ENV_FILE"); envFile != "" {
		key := "PATH"
		if value := os.Getenv("LD_LIBRARY_PATH"); value != "" {
			key = "LD_LIBRARY_PATH"
		} else if value := os.Getenv("DYLD_LIBRARY_PATH"); value != "" {
			key = "DYLD_LIBRARY_PATH"
		}
		_ = os.WriteFile(envFile, []byte(key+"="+os.Getenv(key)), 0o600)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bind helper socket:", err)
		os.Exit(4)
	}
	fmt.Fprintln(os.Stderr, "helper diagnostic")
	handler := http.NewServeMux()
	handler.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		modeBytes, _ := os.ReadFile(os.Getenv("LLAMA_HEALTH_FILE"))
		switch strings.TrimSpace(string(modeBytes)) {
		case "ready":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "crash":
			fmt.Fprintln(os.Stderr, "crash diagnostic")
			os.Exit(23)
		case "fail":
			http.Error(w, "failed", http.StatusInternalServerError)
		default:
			http.Error(w, "loading", http.StatusServiceUnavailable)
		}
	})
	handler.HandleFunc("/props", func(w http.ResponseWriter, _ *http.Request) {
		vision := os.Getenv("LLAMA_VISION") != "false"
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"modalities":{"vision":%s}}`, strconv.FormatBool(vision))
	})
	_ = http.Serve(listener, handler)
}

func newTestSupervisor(t *testing.T, health string) (*Supervisor, string) {
	t.Helper()
	healthFile := filepath.Join(t.TempDir(), "health")
	if err := os.WriteFile(healthFile, []byte(health), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GO_WANT_LLAMA_HELPER", "1")
	t.Setenv("LLAMA_HEALTH_FILE", healthFile)
	t.Setenv("LLAMA_VISION", "true")
	supervisor, err := New(Config{
		RunDir:        filepath.Join(t.TempDir(), "run"),
		Executable:    os.Args[0],
		ModelPath:     "model.gguf",
		ProjectorPath: "mmproj.gguf",
		ContextTokens: 4096,
		StartupTimeout: 5 * time.Second,
		Rand:           func() float64 { return 0.5 },
	})
	if err != nil {
		t.Fatal(err)
	}
	supervisor.pollInterval = 10 * time.Millisecond
	supervisor.probeInterval = 10 * time.Millisecond
	return supervisor, healthFile
}

func waitLlamaState(t *testing.T, supervisor *Supervisor, state proc.State) Status {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := supervisor.Status()
		if status.State == state {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s: %+v", state, supervisor.Status())
	return Status{}
}

func stopLlama(t *testing.T, supervisor *Supervisor) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	supervisor.Stop(ctx)
}

func TestSupervisorLifecycleIsIdempotent(t *testing.T) {
	supervisor, _ := newTestSupervisor(t, "ready")
	supervisor.Start()
	supervisor.Start()
	if err := supervisor.WaitReady(context.Background()); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	first := supervisor.Status()
	if first.State != proc.StateReady || first.PID == 0 || first.Generation != 1 {
		t.Fatalf("ready status = %+v", first)
	}
	if err := supervisor.Available(); err != nil {
		t.Errorf("Available: %v", err)
	}
	response, err := supervisor.Client().Get("http://llama/health")
	if err != nil {
		t.Fatalf("Client health: %v", err)
	}
	response.Body.Close()

	supervisor.Restart()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		status := supervisor.Status()
		if status.Generation > first.Generation && status.State == proc.StateReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status := supervisor.Status(); status.Generation < 2 || status.State != proc.StateReady {
		t.Fatalf("restart did not recover: %+v", status)
	}
	runDir := supervisor.cfg.RunDir
	supervisor.mu.RLock()
	child := supervisor.child
	supervisor.mu.RUnlock()
	if child == nil {
		t.Fatal("ready supervisor has no child")
	}
	stopLlama(t, supervisor)
	stopLlama(t, supervisor)
	select {
	case <-child.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("child remained live after Stop")
	}
	if status := supervisor.Status(); status.State != proc.StateStopped || status.PID != 0 {
		t.Errorf("stopped status = %+v", status)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Errorf("run directory survived Stop: %v", err)
	}
}

func TestWaitReadyBlocksThroughLoading(t *testing.T) {
	supervisor, healthFile := newTestSupervisor(t, "loading")
	supervisor.Start()
	waitLlamaState(t, supervisor, proc.StateStarting)
	done := make(chan error, 1)
	go func() { done <- supervisor.WaitReady(context.Background()) }()
	select {
	case err := <-done:
		t.Fatalf("WaitReady returned during loading: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := os.WriteFile(healthFile, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("WaitReady after ready: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WaitReady did not observe readiness")
	}
	stopLlama(t, supervisor)
}

func TestWaitReadyHonorsCancellation(t *testing.T) {
	supervisor, _ := newTestSupervisor(t, "loading")
	supervisor.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := supervisor.WaitReady(ctx); err == nil || ctx.Err() == nil {
		t.Fatalf("WaitReady = %v, want context cancellation", err)
	}
	stopLlama(t, supervisor)
}

func TestReadinessRequiresVisionModality(t *testing.T) {
	supervisor, _ := newTestSupervisor(t, "ready")
	t.Setenv("LLAMA_VISION", "false")
	supervisor.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := supervisor.WaitReady(ctx)
	if err == nil || !strings.Contains(err.Error(), "does not expose vision") {
		t.Fatalf("WaitReady = %v, want vision configuration failure", err)
	}
	status := waitLlamaState(t, supervisor, proc.StateFailed)
	if status.Generation != 1 || status.LastCrash != nil {
		t.Errorf("vision mismatch entered crash retry state: %+v", status)
	}
	time.Sleep(50 * time.Millisecond)
	if got := supervisor.Status().Generation; got != 1 {
		t.Errorf("vision mismatch crash-looped to generation %d", got)
	}

	t.Setenv("LLAMA_VISION", "true")
	supervisor.Restart()
	if err := supervisor.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady after corrected configuration: %v", err)
	}
	if got := supervisor.Status().Generation; got != 2 {
		t.Errorf("manual recovery generation = %d, want 2", got)
	}
	socket := supervisor.socketPath()
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("bound socket mode = %o, want 600", got)
	}
	stopLlama(t, supervisor)
}

func TestStopBeforeStartIsSafe(t *testing.T) {
	supervisor, _ := newTestSupervisor(t, "ready")
	stopLlama(t, supervisor)
	supervisor.Start()
	if status := supervisor.Status(); status.State != proc.StateStopped {
		t.Errorf("status after Stop then Start = %+v", status)
	}
}

func TestHealthProbeFailuresRecycleAndRecover(t *testing.T) {
	supervisor, healthFile := newTestSupervisor(t, "ready")
	supervisor.backoff = proc.BackoffPolicy{Min: 50 * time.Millisecond, Max: 50 * time.Millisecond}
	supervisor.Start()
	if err := supervisor.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(healthFile, []byte("fail"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := waitLlamaState(t, supervisor, proc.StateBackoff)
	if status.LastCrash == nil || !strings.Contains(status.LastCrash.Error, "3 consecutive health probes") {
		t.Fatalf("health crash report = %+v", status.LastCrash)
	}
	if err := os.WriteFile(healthFile, []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := supervisor.WaitReady(ctx); err != nil {
		t.Fatalf("WaitReady after probe recovery: %v", err)
	}
	if supervisor.Status().Generation < 2 {
		t.Errorf("health failure did not start a replacement: %+v", supervisor.Status())
	}
	stopLlama(t, supervisor)
}

func TestCrashWindowCapsRetriesAndKeepsStderr(t *testing.T) {
	supervisor, healthFile := newTestSupervisor(t, "ready")
	supervisor.backoff = proc.BackoffPolicy{Min: 10 * time.Millisecond, Max: 10 * time.Millisecond}
	supervisor.crashWindow = proc.CrashWindow{Limit: 3, Window: time.Minute}
	supervisor.Start()
	if err := supervisor.WaitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(healthFile, []byte("crash"), 0o600); err != nil {
		t.Fatal(err)
	}
	status := waitLlamaState(t, supervisor, proc.StateFailed)
	if status.Restarts != 3 || status.Generation != 3 {
		t.Errorf("crash cap status = %+v", status)
	}
	if status.LastCrash == nil || !strings.Contains(strings.Join(status.LastCrash.Stderr, "\n"), "crash diagnostic") {
		t.Errorf("stderr crash evidence was lost: %+v", status.LastCrash)
	}
	time.Sleep(50 * time.Millisecond)
	if got := supervisor.Status().Generation; got != 3 {
		t.Errorf("failed supervisor kept restarting to generation %d", got)
	}
	stopLlama(t, supervisor)
}

func TestCommandBuildsPinnedServerContract(t *testing.T) {
	cfg := Config{
		Executable:    filepath.Join(t.TempDir(), "llama-server"),
		ModelPath:     "/models/model.gguf",
		ProjectorPath: "/models/mmproj.gguf",
		ContextTokens: 4096,
	}
	args, env := command(cfg, "/private/llama.sock")
	want := []string{
		"--model", cfg.ModelPath,
		"--mmproj", cfg.ProjectorPath,
		"--host", "/private/llama.sock",
		"--ctx-size", "4096",
		"--threads", strconv.Itoa(onnx.PhysicalCores()),
		"--parallel", "1",
		"--gpu-layers", "0",
		"--no-webui",
		"--log-colors", "off",
		"--log-verbosity", "2",
		"--no-mmproj-offload",
	}
	if got := strings.Join(args, "\x00"); got != strings.Join(want, "\x00") {
		t.Errorf("command args:\n got %q\nwant %q", args, want)
	}
	for _, forbidden := range []string{"--log-disable", "--mmproj-offload"} {
		for _, arg := range args {
			if arg == forbidden {
				t.Errorf("CPU command contains %s", forbidden)
			}
		}
	}

	key := "PATH"
	switch runtime.GOOS {
	case "linux":
		key = "LD_LIBRARY_PATH"
	case "darwin":
		key = "DYLD_LIBRARY_PATH"
	}
	prefix := key + "=" + filepath.Dir(cfg.Executable)
	found := false
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%s does not prepend the server directory: %v", key, env)
	}

	cfg.GPULayers = 27
	cfg.Threads = 3
	args, _ = command(cfg, "/private/llama.sock")
	joined := "\x00" + strings.Join(args, "\x00") + "\x00"
	for _, wantFlag := range []string{"\x00--gpu-layers\x0027\x00", "\x00--threads\x003\x00", "\x00--mmproj-offload\x00"} {
		if !strings.Contains(joined, wantFlag) {
			t.Errorf("GPU command is missing %q: %v", wantFlag, args)
		}
	}
	if strings.Contains(joined, "\x00--no-mmproj-offload\x00") {
		t.Errorf("GPU command disables projector offload: %v", args)
	}
}
