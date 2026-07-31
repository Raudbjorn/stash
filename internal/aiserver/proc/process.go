package proc

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const tokenFD = 3

// StartConfig describes one child process generation.
type StartConfig struct {
	Executable string
	Args       []string
	Env        []string
	Token      string
	Stderr     *RingBuffer
	StderrName string
	Stdout     io.Writer
	Ready      *ReadyConfig
}

// ReadyConfig describes an optional line-based stdout readiness handshake.
type ReadyConfig struct {
	Prefix  string
	Timeout time.Duration
	Decode  func([]byte) error
}

// Process is one started and eventually reaped child.
type Process struct {
	cmd        *exec.Cmd
	exited     chan struct{}
	waitErr    error
	stopMu     sync.Mutex
	tokenInput io.Closer
}

// GenerateToken returns a fresh 32-byte secret encoded as hexadecimal.
func GenerateToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate process token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Start launches a child and optionally waits for a stdout readiness line.
func Start(ctx context.Context, cfg StartConfig) (*Process, error) {
	if cfg.Executable == "" {
		return nil, fmt.Errorf("process executable is empty")
	}
	if cfg.Ready != nil && cfg.Ready.Prefix == "" {
		return nil, fmt.Errorf("readiness prefix is empty")
	}

	cmd := exec.CommandContext(context.WithoutCancel(ctx), cfg.Executable, cfg.Args...)
	cmd.Env = cfg.Env

	tokenRead, tokenWrite, closeAfterWrite, err := configureTokenPipe(cmd, cfg.Token)
	if err != nil {
		return nil, err
	}
	closeTokenPipes := func() {
		if tokenRead != nil {
			_ = tokenRead.Close()
		}
		if tokenWrite != nil {
			_ = tokenWrite.Close()
		}
	}

	var stdout io.ReadCloser
	if cfg.Ready != nil {
		stdout, err = cmd.StdoutPipe()
		if err != nil {
			closeTokenPipes()
			return nil, fmt.Errorf("process stdout: %w", err)
		}
	} else {
		cmd.Stdout = cfg.Stdout
	}

	var stderr io.ReadCloser
	if cfg.Stderr != nil || cfg.StderrName != "" {
		stderr, err = cmd.StderrPipe()
		if err != nil {
			closeTokenPipes()
			return nil, fmt.Errorf("process stderr: %w", err)
		}
	}

	if err := cmd.Start(); err != nil {
		closeTokenPipes()
		return nil, fmt.Errorf("start process: %w", err)
	}
	if tokenRead != nil {
		_ = tokenRead.Close()
		tokenRead = nil
	}
	if tokenWrite != nil {
		go func() {
			if closeAfterWrite {
				defer tokenWrite.Close()
			}
			_, _ = io.WriteString(tokenWrite, cfg.Token+"\n")
		}()
	}

	if stderr != nil {
		cfg.Stderr.Reset()
		go PumpStderr(cfg.StderrName, stderr, cfg.Stderr, nil)
	}

	process := &Process{cmd: cmd, exited: make(chan struct{})}
	if tokenWrite != nil && !closeAfterWrite {
		process.tokenInput = tokenWrite
	}
	go func() {
		process.waitErr = cmd.Wait()
		if process.tokenInput != nil {
			_ = process.tokenInput.Close()
		}
		close(process.exited)
	}()

	if cfg.Ready == nil {
		return process, nil
	}

	ready := make(chan error, 1)
	go pumpStdoutReady(stdout, cfg.Stdout, cfg.Ready, ready)
	timeout := cfg.Ready.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var readyErr error
	select {
	case readyErr = <-ready:
	case <-process.exited:
		select {
		case readyErr = <-ready:
		default:
			readyErr = fmt.Errorf("process exited before reporting ready: %w", process.waitErr)
		}
	case <-timer.C:
		readyErr = fmt.Errorf("process did not report ready within %s", timeout)
	case <-ctx.Done():
		readyErr = ctx.Err()
	}
	if readyErr != nil {
		_ = process.Kill()
		_ = process.Wait()
		return nil, readyErr
	}
	return process, nil
}

func pumpStdoutReady(reader io.ReadCloser, output io.Writer, cfg *ReadyConfig, ready chan<- error) {
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	reported := false
	for scanner.Scan() {
		line := scanner.Text()
		if output != nil {
			_, _ = fmt.Fprintln(output, line)
		}
		if reported {
			continue
		}
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), cfg.Prefix)
		if !ok {
			continue
		}
		reported = true
		if cfg.Decode != nil {
			ready <- cfg.Decode([]byte(rest))
		} else {
			ready <- nil
		}
	}
	if !reported {
		if err := scanner.Err(); err != nil {
			ready <- fmt.Errorf("read process readiness: %w", err)
		} else {
			ready <- fmt.Errorf("process closed stdout without reporting ready")
		}
	}
}

// Stop asks the process to terminate, then kills and reaps it after grace.
func (p *Process) Stop(grace time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	p.stopMu.Lock()
	defer p.stopMu.Unlock()
	select {
	case <-p.exited:
		return p.waitErr
	default:
	}
	_ = signalTerminate(p.cmd)
	if grace > 0 {
		timer := time.NewTimer(grace)
		select {
		case <-p.exited:
			timer.Stop()
			return p.waitErr
		case <-timer.C:
		}
	}
	_ = p.Kill()
	return p.Wait()
}

// Kill terminates the process immediately.
func (p *Process) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	select {
	case <-p.exited:
		return nil
	default:
		return p.cmd.Process.Kill()
	}
}

// Wait returns the cached wait result and is safe to call repeatedly.
func (p *Process) Wait() error {
	if p == nil {
		return nil
	}
	<-p.exited
	return p.waitErr
}

// Exited closes after the process has been reaped.
func (p *Process) Exited() <-chan struct{} {
	if p == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return p.exited
}

// ExitCode returns -1 when the process was signalled or has not exited.
func (p *Process) ExitCode() int {
	if p == nil || p.cmd == nil {
		return -1
	}
	select {
	case <-p.exited:
		if p.cmd.ProcessState == nil {
			return -1
		}
		return p.cmd.ProcessState.ExitCode()
	default:
		return -1
	}
}

// PID returns the child process ID, or zero when unavailable.
func (p *Process) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
