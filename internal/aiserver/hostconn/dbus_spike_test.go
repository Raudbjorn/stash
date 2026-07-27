package hostconn

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
)

// Phase 0.2 gate: prove godbus (Go) interoperates with GDBus (Python) over a
// peer-to-peer unix socket with NO session bus running.
//
// The properties that must hold, because the plugin host design depends on all
// of them:
//   - Dial + Auth with EXTERNAL, and *skip* Hello() (there is no bus daemon to
//     hand out a unique name; calling Hello would hang or error).
//   - Method calls addressed with an empty destination.
//   - Go can Export an object that Python calls back on the SAME connection,
//     so the Bridge does not need a second socket.
//   - Signals propagate from Python to Go.
//
// If this fails, hostconn switches to the JSON-RPC-over-stdio backend before
// anything else depends on the transport choice.

const (
	spikeObjectPath = "/dev/stash/ai/Host"
	spikeIface      = "dev.stash.ai.Host1"
	spikeBridgePath = "/dev/stash/ai/Bridge"
	bridgeIface     = "dev.stash.ai.Bridge1"
	spikeProtocol   = uint32(1)
)

// bridge is the Go-exported object the Python host calls back into.
type bridge struct{ calls chan string }

func (b *bridge) Query(sql, params string) (string, *dbus.Error) {
	b.calls <- sql
	rows, _ := json.Marshal([]map[string]any{{"id": 1, "sql": sql}})
	return string(rows), nil
}

func TestDBusPeerToPeerInterop(t *testing.T) {
	pyHost := filepath.Join("testdata", "dbus_host.py")
	if _, err := os.Stat(pyHost); err != nil {
		t.Skipf("host script not found: %v", err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	// PyGObject is a system package and cannot be assumed present; the feature
	// is designed to degrade rather than fail, so the test skips in kind.
	if out, err := exec.Command("python3", "-c",
		"import gi; gi.require_version('Gio','2.0'); from gi.repository import Gio").CombinedOutput(); err != nil {
		t.Skipf("PyGObject unavailable: %v: %s", err, out)
	}

	// A short socket path: unix socket paths are capped near 108 bytes, and
	// t.TempDir() under a long test name can overflow that.
	dir, err := os.MkdirTemp("", "aispike")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	sock := filepath.Join(dir, "host.sock")
	address := "unix:path=" + sock
	const token = "spike-token-not-a-secret"

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "python3", pyHost, address, token)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start host: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// Readiness handshake: one line of JSON on stdout, the same trick
	// pkg/plugin/raw.go already uses for plugin subprocesses.
	ready := make(chan map[string]any, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			if rest, ok := strings.CutPrefix(line, "AIHOST-READY "); ok {
				var info map[string]any
				if json.Unmarshal([]byte(rest), &info) == nil {
					ready <- info
				}
				return
			}
		}
	}()

	var info map[string]any
	select {
	case info = <-ready:
		t.Logf("host ready: python=%v gi=%v protocol=%v", info["python"], info["gi"], info["protocol"])
	case <-ctx.Done():
		t.Fatal("host did not report AIHOST-READY within the deadline")
	}

	// --- Dial, auth, and deliberately DO NOT call Hello() ---
	conn, err := dbus.Dial(address)
	if err != nil {
		t.Fatalf("dial %s: %v", address, err)
	}
	defer conn.Close()

	if err := conn.Auth([]dbus.Auth{dbus.AuthExternal(currentUID())}); err != nil {
		t.Fatalf("EXTERNAL auth: %v", err)
	}
	// No conn.Hello() - there is no bus daemon. This is the crux of p2p mode.

	// Export the Bridge so Python can call back on this same connection.
	b := &bridge{calls: make(chan string, 1)}
	if err := conn.Export(b, dbus.ObjectPath(spikeBridgePath), bridgeIface); err != nil {
		t.Fatalf("export bridge: %v", err)
	}

	// Subscribe to signals before triggering one.
	signals := make(chan *dbus.Signal, 4)
	conn.Signal(signals)

	// Empty destination - peer-to-peer has no bus names.
	obj := conn.Object("", dbus.ObjectPath(spikeObjectPath))

	t.Run("Hello handshake with token", func(t *testing.T) {
		var version string
		var protocol uint32
		err := obj.CallWithContext(ctx, spikeIface+".Hello", 0, token, spikeProtocol).
			Store(&version, &protocol)
		if err != nil {
			t.Fatalf("Hello: %v", err)
		}
		if protocol != spikeProtocol {
			t.Errorf("protocol = %d, want %d", protocol, spikeProtocol)
		}
		t.Logf("host version %q, protocol %d", version, protocol)
	})

	t.Run("bad token is rejected", func(t *testing.T) {
		err := obj.CallWithContext(ctx, spikeIface+".Hello", 0, "wrong", spikeProtocol).
			Store(new(string), new(uint32))
		if err == nil {
			t.Fatal("Hello accepted a bad token")
		}
		t.Logf("rejected as expected: %v", err)
	})

	t.Run("Ping", func(t *testing.T) {
		var ns uint64
		if err := obj.CallWithContext(ctx, spikeIface+".Ping", 0).Store(&ns); err != nil {
			t.Fatalf("Ping: %v", err)
		}
		if ns == 0 {
			t.Error("Ping returned zero monotonic time")
		}
	})

	t.Run("reverse call into Go-exported Bridge", func(t *testing.T) {
		const sql = "SELECT id FROM p_myplugin_scores"
		var rowsJSON string
		if err := obj.CallWithContext(ctx, spikeIface+".CallBridge", 0, sql).Store(&rowsJSON); err != nil {
			t.Fatalf("CallBridge: %v", err)
		}

		select {
		case got := <-b.calls:
			if got != sql {
				t.Errorf("bridge received %q, want %q", got, sql)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Go-exported Bridge was never called")
		}

		var rows []map[string]any
		if err := json.Unmarshal([]byte(rowsJSON), &rows); err != nil {
			t.Fatalf("bridge result not JSON: %v", err)
		}
		if len(rows) != 1 || rows[0]["sql"] != sql {
			t.Errorf("round-trip lost data: %v", rows)
		}
	})

	t.Run("signal from host to Go", func(t *testing.T) {
		const invID = "inv-42"
		if err := obj.CallWithContext(ctx, spikeIface+".EmitProgress", 0, invID, 0.75).Store(); err != nil {
			t.Fatalf("EmitProgress: %v", err)
		}

		deadline := time.After(5 * time.Second)
		for {
			select {
			case sig := <-signals:
				if sig.Name != spikeIface+".Progress" {
					continue
				}
				if len(sig.Body) != 2 {
					t.Fatalf("Progress body = %v", sig.Body)
				}
				if sig.Body[0] != invID {
					t.Errorf("invocation id = %v, want %v", sig.Body[0], invID)
				}
				if f, ok := sig.Body[1].(float64); !ok || f != 0.75 {
					t.Errorf("fraction = %v, want 0.75", sig.Body[1])
				}
				return
			case <-deadline:
				t.Fatal("no Progress signal received")
			}
		}
	})
}

func currentUID() string {
	return itoa(os.Getuid())
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
