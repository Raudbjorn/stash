package host

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/proc"
)

func requirePython(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	return path
}

func echoScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", "echo_host.py"))
	if err != nil {
		t.Fatalf("resolve script: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("echo host missing: %v", err)
	}
	return path
}

func spawnEcho(t *testing.T, behave string) (*proc.Process, ReadyInfo, *proc.RingBuffer, error) {
	t.Helper()
	python := requirePython(t)
	buffer := proc.NewRingBuffer(50)
	dir, err := os.MkdirTemp("", "aihost")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	address, _, err := socketAddress(filepath.Join(dir, "run"))
	if err != nil {
		t.Fatalf("socket address: %v", err)
	}
	token, err := proc.GenerateToken()
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	t.Cleanup(cancel)
	t.Setenv("ECHO_BEHAVE", behave)
	process, info, err := spawn(ctx, spawnConfig{
		Interpreter: python,
		Script:      echoScript(t),
		Address:     address,
		Token:       token,
	}, buffer)
	if process != nil {
		t.Cleanup(func() { _ = process.Stop(time.Second) })
	}
	return process, info, buffer, err
}

func TestSpawnReportsReady(t *testing.T) {
	process, info, _, err := spawnEcho(t, "serve")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if info.PID == 0 || info.Protocol != 1 || info.Python == "" {
		t.Errorf("incomplete readiness info: %+v", info)
	}
	if process.PID() == 0 {
		t.Error("process has no pid")
	}
}

func TestTokenIsPassedOnDescriptorThree(t *testing.T) {
	_, _, buffer, err := spawnEcho(t, "no-token")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var received string
	for time.Now().Before(deadline) {
		for _, line := range buffer.Snapshot() {
			if rest, ok := strings.CutPrefix(line, "token-received:"); ok {
				received = rest
			}
		}
		if received != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(received) != 64 {
		t.Errorf("descriptor token is %d hex chars, want 64", len(received))
	}
}

func TestSpawnFailsWhenChildExitsBeforeReady(t *testing.T) {
	_, _, buffer, err := spawnEcho(t, "crash-now")
	if err == nil {
		t.Fatal("spawn should fail when the child exits before reporting ready")
	}
	if len(buffer.Snapshot()) == 0 {
		t.Error("no stderr was captured from the failed child")
	}
}

func TestSpawnTimesOutWithoutReadiness(t *testing.T) {
	python := requirePython(t)
	buffer := proc.NewRingBuffer(20)
	dir, err := os.MkdirTemp("", "aihost")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	address, _, err := socketAddress(filepath.Join(dir, "run"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := proc.GenerateToken()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ECHO_BEHAVE", "no-ready")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	started := time.Now()
	process, _, err := spawn(ctx, spawnConfig{
		Interpreter: python, Script: echoScript(t), Address: address, Token: token,
	}, buffer)
	if process != nil {
		_ = process.Stop(time.Second)
	}
	if err == nil {
		t.Fatal("spawn should fail when the host never reports ready")
	}
	if elapsed := time.Since(started); elapsed > 25*time.Second {
		t.Errorf("spawn waited %v; it should honour its context", elapsed)
	}
}

func TestSpawnRejectsMalformedReadiness(t *testing.T) {
	_, _, _, err := spawnEcho(t, "bad-ready")
	if err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error = %v, want malformed readiness failure", err)
	}
}

func TestStopTerminatesProcess(t *testing.T) {
	process, _, _, err := spawnEcho(t, "serve")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	started := time.Now()
	_ = process.Stop(2 * time.Second)
	select {
	case <-process.Exited():
	case <-time.After(5 * time.Second):
		t.Fatal("process did not exit after stop")
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Errorf("stop took %v; the polite signal should have been enough", elapsed)
	}
}

func TestStderrTailIsRetainedForCrashReports(t *testing.T) {
	_, _, buffer, err := spawnEcho(t, "noisy")
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var lines []string
	for time.Now().Before(deadline) {
		lines = buffer.Snapshot()
		if containsLine(lines, "RuntimeError: simulated plugin failure") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !containsLine(lines, "RuntimeError: simulated plugin failure") ||
		!containsLine(lines, "Traceback (most recent call last):") {
		t.Errorf("the traceback was not retained: %v", lines)
	}
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if strings.Contains(line, want) {
			return true
		}
	}
	return false
}
