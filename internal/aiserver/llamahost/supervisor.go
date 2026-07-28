package llamahost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"github.com/stashapp/stash/internal/aiserver/proc"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/onnx"
)

const (
	defaultStartupTimeout = 5 * time.Minute
	defaultProbeInterval  = 10 * time.Second
	defaultPollInterval   = 250 * time.Millisecond
	failedProbeLimit      = 3
	stopGrace             = 5 * time.Second
)

var ErrClosed = errors.New("llama-server supervisor is closed")

// Config configures one supervised llama-server.
type Config struct {
	RunDir, Executable, ModelPath, ProjectorPath string
	ContextTokens, GPULayers, Threads            int
	StartupTimeout                               time.Duration
	Rand                                         func() float64
}

// Status is the supervisor's live externally visible state.
type Status struct {
	State       proc.State        `json:"state"`
	Message     string            `json:"message,omitempty"`
	Remediation string            `json:"remediation,omitempty"`
	Generation  int               `json:"generation"`
	PID         int               `json:"pid,omitempty"`
	Restarts    int               `json:"restarts"`
	NextRestart *time.Time        `json:"next_restart,omitempty"`
	LastCrash   *proc.CrashReport `json:"last_crash,omitempty"`
}

// Supervisor owns a llama-server process and its private Unix transport.
type Supervisor struct {
	cfg Config

	mu      sync.RWMutex
	status  Status
	started bool
	closed  bool
	changed chan struct{}
	child   *proc.Process
	socket  string

	stderr      *proc.RingBuffer
	backoff     proc.BackoffPolicy
	crashWindow proc.CrashWindow

	client        *http.Client
	transport     *http.Transport
	pollInterval  time.Duration
	probeInterval time.Duration

	stopCh   chan struct{}
	restart  chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
	doneOnce sync.Once
}

// New validates configuration and constructs a stopped supervisor.
func New(cfg Config) (*Supervisor, error) {
	switch {
	case cfg.RunDir == "":
		return nil, fmt.Errorf("llama-server run directory is empty")
	case cfg.Executable == "":
		return nil, fmt.Errorf("llama-server executable is empty")
	case cfg.ModelPath == "":
		return nil, fmt.Errorf("llama-server model path is empty")
	case cfg.ProjectorPath == "":
		return nil, fmt.Errorf("llama-server projector path is empty")
	case cfg.ContextTokens <= 0:
		return nil, fmt.Errorf("llama-server context tokens must be positive")
	case cfg.GPULayers < 0:
		return nil, fmt.Errorf("llama-server GPU layers cannot be negative")
	case cfg.Threads < 0:
		return nil, fmt.Errorf("llama-server threads cannot be negative")
	}
	if cfg.StartupTimeout <= 0 {
		cfg.StartupTimeout = defaultStartupTimeout
	}

	s := &Supervisor{
		cfg:           cfg,
		status:        Status{State: proc.StateStopped, Message: "llama-server is stopped."},
		changed:       make(chan struct{}),
		stderr:        proc.NewRingBuffer(200),
		backoff:       proc.DefaultBackoffPolicy(),
		crashWindow:   proc.CrashWindow{Limit: 10, Window: 10 * time.Minute},
		pollInterval:  defaultPollInterval,
		probeInterval: defaultProbeInterval,
		stopCh:        make(chan struct{}),
		restart:       make(chan struct{}, 1),
		doneCh:        make(chan struct{}),
	}
	dialer := &net.Dialer{}
	s.transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			s.mu.RLock()
			socket := s.socket
			s.mu.RUnlock()
			if socket == "" {
				return nil, fmt.Errorf("llama-server socket is not allocated")
			}
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	s.client = &http.Client{Transport: s.transport}
	return s, nil
}

// Start begins supervision. It is safe to call repeatedly.
func (s *Supervisor) Start() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started || s.closed {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	go s.run()
}

// Stop closes supervision and waits for the child to be reaped.
func (s *Supervisor) Stop(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.closed = true
		s.status.State = proc.StateStopped
		s.status.Message = "llama-server is stopped."
		s.signalLocked()
		s.mu.Unlock()
		s.doneOnce.Do(func() { close(s.doneCh) })
		return
	}
	s.mu.Unlock()
	s.stopOnce.Do(func() { close(s.stopCh) })
	select {
	case <-s.doneCh:
	case <-ctx.Done():
	}
}

// Restart requests immediate process recycling or recovery from Failed.
func (s *Supervisor) Restart() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if s.status.State == proc.StateFailed {
		s.status.State = proc.StateStopped
		s.status.Message = "Restarting llama-server."
		s.status.Remediation = ""
		s.signalLocked()
	}
	s.mu.Unlock()
	select {
	case s.restart <- struct{}{}:
	default:
	}
}

// Available returns an immediate health snapshot.
func (s *Supervisor) Available() error {
	if s == nil {
		return ErrClosed
	}
	status := s.Status()
	if status.State == proc.StateReady {
		return nil
	}
	if status.Remediation != "" {
		return fmt.Errorf("%s %s", status.Message, status.Remediation)
	}
	return errors.New(status.Message)
}

// WaitReady waits through startup and backoff until ready or terminal.
func (s *Supervisor) WaitReady(ctx context.Context) error {
	if s == nil {
		return ErrClosed
	}
	for {
		s.mu.RLock()
		status, closed, changed := s.status, s.closed, s.changed
		s.mu.RUnlock()
		if status.State == proc.StateReady {
			return nil
		}
		if closed {
			return ErrClosed
		}
		if status.State.Terminal() {
			return s.Available()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// Client returns the preconfigured Unix-socket HTTP client.
func (s *Supervisor) Client() *http.Client {
	if s == nil {
		return nil
	}
	return s.client
}

// Status returns a concurrency-safe snapshot.
func (s *Supervisor) Status() Status {
	if s == nil {
		return Status{State: proc.StateStopped, Message: ErrClosed.Error()}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	status := s.status
	if s.status.LastCrash != nil {
		last := *s.status.LastCrash
		last.Stderr = append([]string(nil), last.Stderr...)
		status.LastCrash = &last
	}
	return status
}

func (s *Supervisor) run() {
	defer func() {
		s.stopChild()
		s.mu.Lock()
		s.closed = true
		s.status.State = proc.StateStopped
		s.status.Message = "llama-server is stopped."
		s.status.PID = 0
		s.status.NextRestart = nil
		s.signalLocked()
		s.mu.Unlock()
		s.doneOnce.Do(func() { close(s.doneCh) })
	}()

	attempt := 0
	for {
		if s.stopping() {
			return
		}
		child, err := s.launchGeneration()
		if err != nil {
			s.stopChild()
			s.recordCrash(-1, err)
			if !s.recoverAfterCrash(&attempt) {
				return
			}
			continue
		}

		err = s.awaitReady(child)
		if err != nil {
			s.stopChild()
			if errors.Is(err, ErrClosed) {
				return
			}
			if errors.Is(err, errRestartRequested) {
				continue
			}
			var configErr *configurationError
			if errors.As(err, &configErr) {
				s.setFailed(configErr.Error(), configErr.remediation)
				if !s.waitForManualRestart() {
					return
				}
				attempt = 0
				continue
			}
			s.recordCrash(child.ExitCode(), err)
			if !s.recoverAfterCrash(&attempt) {
				return
			}
			continue
		}

		attempt = 0
		s.setReady(child)
		reason, monitorErr := s.monitor(child)
		s.stopChild()
		switch reason {
		case generationStopped:
			return
		case generationRestarted:
			continue
		case generationCrashed:
			s.recordCrash(child.ExitCode(), monitorErr)
			if !s.recoverAfterCrash(&attempt) {
				return
			}
		}
	}
}

func (s *Supervisor) launchGeneration() (*proc.Process, error) {
	socket, err := proc.PrivateUnixSocket(s.cfg.RunDir, "llama.sock")
	if err != nil {
		return nil, err
	}
	args, env := command(s.cfg, socket)

	s.mu.Lock()
	s.socket = socket
	s.status.Generation++
	s.status.State = proc.StateStarting
	s.status.Message = "Loading the vision-language model."
	s.status.Remediation = ""
	s.status.NextRestart = nil
	s.signalLocked()
	s.mu.Unlock()

	child, err := proc.Start(context.Background(), proc.StartConfig{
		Executable: s.cfg.Executable,
		Args:       args,
		Env:        env,
		Stderr:     s.stderr,
		StderrName: "llama-server",
	})
	if err != nil {
		return nil, fmt.Errorf("start llama-server: %w", err)
	}
	s.mu.Lock()
	s.child = child
	s.status.PID = child.PID()
	s.signalLocked()
	s.mu.Unlock()
	return child, nil
}

func command(cfg Config, socket string) ([]string, []string) {
	threads := cfg.Threads
	if threads == 0 {
		threads = onnx.PhysicalCores()
	}
	args := []string{
		"--model", cfg.ModelPath,
		"--mmproj", cfg.ProjectorPath,
		"--host", socket,
		"--ctx-size", strconv.Itoa(cfg.ContextTokens),
		"--threads", strconv.Itoa(threads),
		"--parallel", "1",
		"--gpu-layers", strconv.Itoa(cfg.GPULayers),
		"--no-webui",
		"--log-colors", "off",
		"--log-verbosity", "2",
	}
	if cfg.GPULayers > 0 {
		args = append(args, "--mmproj-offload")
	} else {
		args = append(args, "--no-mmproj-offload")
	}
	key := "PATH"
	switch runtime.GOOS {
	case "linux":
		key = "LD_LIBRARY_PATH"
	case "darwin":
		key = "DYLD_LIBRARY_PATH"
	}
	env := proc.AppendPathEnv(os.Environ(), key, filepath.Dir(cfg.Executable))
	return args, env
}

func (s *Supervisor) setReady(child *proc.Process) {
	s.mu.Lock()
	if s.child == child {
		s.status.State = proc.StateReady
		s.status.Message = "Vision-language model ready."
		s.status.Remediation = ""
		s.status.NextRestart = nil
		s.signalLocked()
	}
	s.mu.Unlock()
	logger.Infof("llama-server ready: generation %d, pid %d", s.Status().Generation, child.PID())
}

func (s *Supervisor) recordCrash(exitCode int, failure error) {
	now := time.Now()
	s.mu.Lock()
	report := &proc.CrashReport{
		Generation: s.status.Generation,
		ExitCode:   exitCode,
		Error:      failure.Error(),
		Stderr:     s.stderr.Snapshot(),
		At:         now,
	}
	s.crashWindow.Record(now)
	s.status.LastCrash = report
	s.status.State = proc.StateCrashed
	s.status.Message = failure.Error()
	s.status.Restarts = s.crashWindow.Count(now)
	s.status.Remediation = stderrRemediation(report.Stderr)
	s.signalLocked()
	s.mu.Unlock()
	logger.Errorf("llama-server crashed (generation %d, exit %d): %v", report.Generation, exitCode, failure)
}

func stderrRemediation(lines []string) string {
	if len(lines) == 0 {
		return "Check that the selected llama.cpp build and model pair are compatible."
	}
	return "llama-server stderr: " + lines[len(lines)-1]
}

func (s *Supervisor) recoverAfterCrash(attempt *int) bool {
	s.mu.Lock()
	count := s.crashWindow.Count(time.Now())
	limit := s.crashWindow.Limit
	s.mu.Unlock()
	if count >= limit {
		s.setFailed(
			fmt.Sprintf("llama-server crashed %d times in %s; not retrying.", limit, s.crashWindow.Window),
			"Check the stderr crash tail, then restart the VLM host manually.")
		if !s.waitForManualRestart() {
			return false
		}
		*attempt = 0
		return true
	}

	delay := s.backoff.Delay(*attempt, s.randFunc())
	(*attempt)++
	next := time.Now().Add(delay)
	s.mu.Lock()
	s.status.State = proc.StateBackoff
	s.status.Message = fmt.Sprintf("Restarting llama-server at %s.", next.Format(time.TimeOnly))
	s.status.NextRestart = &next
	s.status.Restarts = count
	s.signalLocked()
	s.mu.Unlock()
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-s.stopCh:
		return false
	case <-s.restart:
		*attempt = 0
		return true
	case <-timer.C:
		return true
	}
}

func (s *Supervisor) waitForManualRestart() bool {
	select {
	case <-s.stopCh:
		return false
	case <-s.restart:
		s.mu.Lock()
		s.crashWindow.Reset()
		s.status.Restarts = 0
		s.mu.Unlock()
		return true
	}
}

func (s *Supervisor) setFailed(message, remediation string) {
	s.mu.Lock()
	s.status.State = proc.StateFailed
	s.status.Message = message
	s.status.Remediation = remediation
	s.status.NextRestart = nil
	s.signalLocked()
	s.mu.Unlock()
}

func (s *Supervisor) stopChild() {
	s.mu.Lock()
	child, socket := s.child, s.socket
	s.child, s.socket = nil, ""
	if child != nil {
		s.status.State = proc.StateStopping
		s.status.Message = "Stopping llama-server."
		s.signalLocked()
	}
	s.mu.Unlock()
	if child != nil {
		_ = child.Stop(stopGrace)
	}
	if socket != "" {
		_ = os.Remove(socket)
		if filepath.Clean(filepath.Dir(socket)) != filepath.Clean(s.cfg.RunDir) {
			_ = os.RemoveAll(filepath.Dir(socket))
		}
	}
	_ = os.RemoveAll(s.cfg.RunDir)
	s.mu.Lock()
	s.status.PID = 0
	s.mu.Unlock()
}

func (s *Supervisor) stopping() bool {
	select {
	case <-s.stopCh:
		return true
	default:
		return false
	}
}

func (s *Supervisor) randFunc() func() float64 {
	if s.cfg.Rand != nil {
		return s.cfg.Rand
	}
	return rand.Float64
}

func (s *Supervisor) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

func discardBody(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64*1024))
	_ = body.Close()
}
