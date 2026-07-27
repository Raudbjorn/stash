package proc

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRingBufferOldestFirstAndReset(t *testing.T) {
	buffer := NewRingBuffer(3)
	for _, line := range []string{"one", "two", "three", "four"} {
		buffer.Add(line)
	}
	if got := strings.Join(buffer.Snapshot(), ","); got != "two,three,four" {
		t.Errorf("Snapshot = %q, want oldest-first bounded tail", got)
	}
	buffer.Reset()
	if got := buffer.Snapshot(); len(got) != 0 {
		t.Errorf("Snapshot after Reset = %v", got)
	}
}

func TestPumpStderrSuppressesBlankLinesAndAcceptsLargeLines(t *testing.T) {
	large := strings.Repeat("x", 128*1024)
	reader := io.NopCloser(strings.NewReader("\nfirst\r\n" + large + "\n"))
	buffer := NewRingBuffer(4)
	PumpStderr("helper-test", reader, buffer, nil)
	got := buffer.Snapshot()
	if len(got) != 2 || got[0] != "first" || got[1] != large {
		t.Errorf("stderr tail contains wrong lines or order: lengths=%v", lineLengths(got))
	}
}

func lineLengths(lines []string) []int {
	out := make([]int, len(lines))
	for i, line := range lines {
		out[i] = len(line)
	}
	return out
}

func TestAppendPathEnvPrepends(t *testing.T) {
	env := []string{"OTHER=value", "PATH=old"}
	got := AppendPathEnv(env, "PATH", "new")
	want := "PATH=new" + string(os.PathListSeparator) + "old"
	if got[1] != want {
		t.Errorf("PATH = %q, want %q", got[1], want)
	}
	got = AppendPathEnv(nil, "PYTHONPATH", "runtime")
	if len(got) != 1 || got[0] != "PYTHONPATH=runtime" {
		t.Errorf("new path env = %v", got)
	}
}

func TestPrivateUnixSocketSecuresAndClearsPath(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), "run")
	socket, err := PrivateUnixSocket(runDir, "host.sock")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("run directory mode = %o, want 700", got)
	}
	if err := os.WriteFile(socket, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := PrivateUnixSocket(runDir, "host.sock")
	if err != nil {
		t.Fatal(err)
	}
	if again != socket {
		t.Errorf("socket path changed: %q != %q", again, socket)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Errorf("stale socket remains: %v", err)
	}
}

func TestPrivateUnixSocketUsesDeterministicShortFallback(t *testing.T) {
	runDir := filepath.Join(t.TempDir(), strings.Repeat("long", 35))
	first, err := PrivateUnixSocket(runDir, "llama.sock")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(first) != runDir {
		t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(first)) })
	}
	second, err := PrivateUnixSocket(runDir, "llama.sock")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("fallback is not deterministic: %q != %q", first, second)
	}
	if len(first) > MaxUnixSocketPath || !strings.HasSuffix(first, ".sock") {
		t.Errorf("fallback path %q does not fit the AF_UNIX contract", first)
	}
	info, err := os.Stat(filepath.Dir(first))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("fallback directory mode = %o, want 700", got)
	}
}
