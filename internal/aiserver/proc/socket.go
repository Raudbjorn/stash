package proc

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// MaxUnixSocketPath is the conservative cross-platform sun_path byte ceiling.
const MaxUnixSocketPath = 103

// PrivateUnixSocket prepares a private directory and returns a stale-free socket path.
func PrivateUnixSocket(runDir, name string) (string, error) {
	if name == "" || filepath.Base(name) != name || strings.ContainsAny(name, `/\\`) {
		return "", fmt.Errorf("invalid unix socket name %q", name)
	}
	if err := secureDirectory(runDir); err != nil {
		return "", err
	}

	socket := filepath.Join(runDir, name)
	if len(socket) > MaxUnixSocketPath {
		short, err := shortSocketPath(runDir, name)
		if err != nil {
			return "", err
		}
		socket = short
	}
	if err := os.Remove(socket); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("remove stale unix socket: %w", err)
	}
	return socket, nil
}

func shortSocketPath(runDir, name string) (string, error) {
	base := os.TempDir()
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" && len(xdg) < len(base) {
		base = xdg
	}
	sum := sha256.Sum256([]byte(socketOwnerKey() + "\x00" + runDir + "\x00" + name))
	dir := filepath.Join(base, "stash-ai-"+hex.EncodeToString(sum[:])[:12])
	if err := secureDirectory(dir); err != nil {
		return "", fmt.Errorf("create short socket directory: %w", err)
	}
	socket := filepath.Join(dir, "s.sock")
	if len(socket) > MaxUnixSocketPath {
		return "", fmt.Errorf(
			"no socket path on this system is short enough: %q is %d bytes and the kernel limit is %d",
			socket, len(socket), MaxUnixSocketPath)
	}
	return socket, nil
}

func secureDirectory(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create run directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure run directory: %w", err)
	}
	return nil
}
