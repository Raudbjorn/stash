package host

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/stashapp/stash/internal/aiserver/proc"
)

const readyPrefix = "AIHOST-READY "
const tokenFD = 3

// ReadyInfo is what a host reports when it comes up.
type ReadyInfo struct {
	Address  string `json:"address"`
	PID      int    `json:"pid"`
	Protocol int    `json:"protocol"`
	Python   string `json:"python"`
	GI       string `json:"gi"`
}

type spawnConfig struct {
	Interpreter string
	Module      string
	Script      string
	Address     string
	Token       string
	PluginDir   string
	RuntimeDir  string
	LogLevel    string
}

// socketAddress adds the D-Bus transport decoration to a private socket path.
func socketAddress(runDir string) (string, string, error) {
	if runtime.GOOS == "windows" {
		// GLib's nonce-tcp transport binds loopback and authenticates clients
		// with a generated nonce file, providing the Windows equivalent of the
		// private Unix socket.
		return "nonce-tcp:host=127.0.0.1,port=0", "", nil
	}
	socket, err := proc.PrivateUnixSocket(runDir, "host.sock")
	if err != nil {
		return "", "", err
	}
	return "unix:path=" + socket, socket, nil
}

// spawn adapts the Python host's argv and readiness JSON to the generic process.
func spawn(ctx context.Context, cfg spawnConfig, stderr *proc.RingBuffer) (*proc.Process, ReadyInfo, error) {
	var args []string
	if cfg.Module != "" {
		args = append(args, "-m", cfg.Module)
	} else {
		args = append(args, cfg.Script)
	}
	args = append(args, "--address", cfg.Address)
	if runtime.GOOS == "windows" {
		args = append(args, "--token-stdin")
	} else {
		args = append(args, "--token-fd", fmt.Sprint(tokenFD))
	}
	if cfg.LogLevel != "" {
		args = append(args, "--log-level", cfg.LogLevel)
	}
	if cfg.PluginDir != "" {
		args = append(args, "--plugins-dir", cfg.PluginDir)
	}

	env := os.Environ()
	if cfg.RuntimeDir != "" {
		env = proc.AppendPathEnv(env, "PYTHONPATH", cfg.RuntimeDir)
	}
	env = append(env, "PYTHONUNBUFFERED=1", "PYTHONDONTWRITEBYTECODE=1")

	var info ReadyInfo
	process, err := proc.Start(ctx, proc.StartConfig{
		Executable: cfg.Interpreter,
		Args:       args,
		Env:        env,
		Token:      cfg.Token,
		Stderr:     stderr,
		StderrName: "ai-plugin-host",
		Ready: &proc.ReadyConfig{
			Prefix:  readyPrefix,
			Timeout: readyTimeout,
			Decode: func(payload []byte) error {
				if err := json.Unmarshal(payload, &info); err != nil {
					return fmt.Errorf("malformed readiness line: %w", err)
				}
				return nil
			},
		},
	})
	if err != nil {
		return nil, ReadyInfo{}, err
	}
	return process, info, nil
}
