//go:build !windows

package proc

import (
	"os/exec"
	"syscall"
)

func signalTerminate(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Signal(syscall.SIGTERM)
}
