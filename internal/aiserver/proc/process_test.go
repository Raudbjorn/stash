package proc

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProcessHelper(t *testing.T) {
	if os.Getenv("GO_WANT_PROC_HELPER") != "1" {
		return
	}
	mode := ""
	for i, arg := range os.Args {
		if arg == "--" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
			break
		}
	}
	switch mode {
	case "ready":
		token, _ := io.ReadAll(os.NewFile(tokenFD, "token"))
		fmt.Println("noise")
		fmt.Printf("READY %s\n", strings.TrimSpace(string(token)))
		fmt.Println("after")
		fmt.Fprintln(os.Stderr, "child diagnostic")
		time.Sleep(50 * time.Millisecond)
		os.Exit(7)
	case "sleep":
		time.Sleep(time.Minute)
	case "never-ready":
		fmt.Println("not ready")
		time.Sleep(time.Minute)
	default:
		os.Exit(2)
	}
}

func helperConfig(mode string) StartConfig {
	return StartConfig{
		Executable: os.Args[0],
		Args:       []string{"-test.run=TestProcessHelper", "--", mode},
		Env:        append(os.Environ(), "GO_WANT_PROC_HELPER=1"),
	}
}

func TestStartReadyTokenAndCachedWait(t *testing.T) {
	cfg := helperConfig("ready")
	cfg.Token = "secret-token"
	cfg.Stderr = NewRingBuffer(4)
	cfg.StderrName = "process-test"
	var stdout bytes.Buffer
	cfg.Stdout = &stdout
	cfg.Ready = &ReadyConfig{
		Prefix:  "READY ",
		Timeout: 5 * time.Second,
		Decode: func(payload []byte) error {
			if got := string(payload); got != cfg.Token {
				return fmt.Errorf("readiness token = %q", got)
			}
			return nil
		},
	}

	process, err := Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if process.PID() == 0 {
		t.Fatal("started process has no PID")
	}
	first := process.Wait()
	second := process.Wait()
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("cached Wait results differ: first=%v second=%v", first, second)
	}
	if got := process.ExitCode(); got != 7 {
		t.Errorf("ExitCode = %d, want 7", got)
	}
	if !strings.Contains(stdout.String(), "noise\n") || !strings.Contains(stdout.String(), "after\n") {
		t.Errorf("stdout was not drained around readiness: %q", stdout.String())
	}
	if got := cfg.Stderr.Snapshot(); len(got) != 1 || got[0] != "child diagnostic" {
		t.Errorf("stderr tail = %v", got)
	}
}

func TestStartupContextDoesNotOwnChildLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	process, err := Start(ctx, helperConfig("sleep"))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancel()
	select {
	case <-process.Exited():
		t.Fatal("canceling the startup context killed the child")
	case <-time.After(100 * time.Millisecond):
	}
	if err := process.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	_ = process.Wait()
}

func TestReadinessFailureKillsAndReaps(t *testing.T) {
	cfg := helperConfig("never-ready")
	cfg.Ready = &ReadyConfig{Prefix: "READY ", Timeout: 50 * time.Millisecond}
	started := time.Now()
	process, err := Start(context.Background(), cfg)
	if err == nil || process != nil {
		t.Fatalf("Start = (%v, %v), want a readiness failure", process, err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Errorf("readiness cleanup took %v", elapsed)
	}
}

func TestStopAndWaitAreRepeatable(t *testing.T) {
	process, err := Start(context.Background(), helperConfig("sleep"))
	if err != nil {
		t.Fatal(err)
	}
	if got := process.ExitCode(); got != -1 {
		t.Fatalf("live process ExitCode = %d, want -1", got)
	}
	first := process.Stop(10 * time.Millisecond)
	second := process.Stop(10 * time.Millisecond)
	if (first == nil) != (second == nil) {
		t.Fatalf("repeat Stop results differ: %v, %v", first, second)
	}
	if first != nil && first.Error() != second.Error() {
		t.Fatalf("repeat Stop errors differ: %v, %v", first, second)
	}
}
