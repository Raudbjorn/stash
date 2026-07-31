//go:build !windows

package proc

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// configureTokenPipe passes the read end as descriptor 3. The parent closes its
// write end after sending the token; Unix process inheritance keeps the secret
// out of argv and the environment.
func configureTokenPipe(cmd *exec.Cmd, token string) (*os.File, io.WriteCloser, bool, error) {
	if token == "" {
		return nil, nil, false, nil
	}
	read, write, err := os.Pipe()
	if err != nil {
		return nil, nil, false, fmt.Errorf("create token pipe: %w", err)
	}
	cmd.ExtraFiles = []*os.File{read}
	return read, write, true, nil
}
