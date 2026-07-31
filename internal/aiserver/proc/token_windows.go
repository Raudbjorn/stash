//go:build windows

package proc

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// configureTokenPipe uses the child's inherited standard-input handle because
// exec.Cmd.ExtraFiles is unsupported on Windows. The writer stays open after
// the token line so the child can also use EOF to detect that its parent died.
func configureTokenPipe(cmd *exec.Cmd, token string) (*os.File, io.WriteCloser, bool, error) {
	if token == "" {
		return nil, nil, false, nil
	}
	write, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, false, fmt.Errorf("create token stdin pipe: %w", err)
	}
	return nil, write, false, nil
}
