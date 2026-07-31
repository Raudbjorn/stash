package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/stashapp/stash/internal/aiserver/proc"
)

// The kernel caps unix socket paths near 104 bytes, bind fails with EADDRINUSE
// when they are too long, and GLib surfaces that as "Address already in use" -
// which sends you looking for a stale socket that does not exist. Stash's
// config directory is chosen by the user and can be arbitrarily deep, so this
// is a real failure mode rather than a theoretical one.
func TestSocketAddressFallsBackFromALongPath(t *testing.T) {
	if os.Getenv("GOOS") == "windows" {
		t.Skip("unix sockets only")
	}

	// A run directory comfortably past the limit.
	deep := filepath.Join(t.TempDir(), strings.Repeat("a-fairly-long-directory-name/", 6))
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if len(filepath.Join(deep, "host.sock")) <= proc.MaxUnixSocketPath {
		t.Fatalf("the test path is not long enough to exercise the fallback: %d bytes", len(deep))
	}

	address, sock, err := socketAddress(deep)
	if err != nil {
		t.Fatalf("socketAddress: %v", err)
	}
	if len(sock) > proc.MaxUnixSocketPath {
		t.Errorf("fallback path is still %d bytes: %s", len(sock), sock)
	}
	if !strings.HasPrefix(address, "unix:path=") {
		t.Errorf("address = %q", address)
	}

	// The fallback must keep the security properties of the run directory:
	// the socket is an authenticated channel into Stash.
	info, err := os.Stat(filepath.Dir(sock))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("socket directory mode = %o, want 700", perm)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(sock)) })
}

// A short path must be used as-is, so the socket stays beside the rest of the
// AI subsystem's state where an operator would look for it.
func TestSocketAddressKeepsAShortPath(t *testing.T) {
	dir, err := os.MkdirTemp("", "aish*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	_, sock, err := socketAddress(dir)
	if err != nil {
		t.Fatalf("socketAddress: %v", err)
	}
	if filepath.Dir(sock) != dir {
		t.Errorf("a short path was relocated: %s", sock)
	}
}
