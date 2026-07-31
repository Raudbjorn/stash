package host

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stashapp/stash/internal/aiserver/proc"
	"github.com/stashapp/stash/internal/aiserver/pyenv"
	"github.com/stashapp/stash/pkg/logger"
)

// Config configures the supervisor.
type Config struct {
	// Enabled switches plugin hosting on. When false nothing is spawned.
	Enabled bool

	// BaseDir is the AI subsystem's directory. The venv, run directory and
	// extracted runtime all live beneath it.
	BaseDir string

	// PluginDir holds installed plugins.
	PluginDir string

	// ConfiguredPython is Stash's python_path setting.
	ConfiguredPython string

	// LogLevel is passed to the host.
	LogLevel string

	// Connect performs the authenticated handshake once a host reports ready.
	// Returning an error fails the generation and triggers a restart.
	Connect func(ctx context.Context, address, token string) (Conn, error)

	// PrepareEnv overrides how the Python environment is obtained. Nil uses the
	// real pyenv manager; tests substitute one to exercise both the usable and
	// unusable paths without depending on the machine they run on.
	PrepareEnv func(ctx context.Context) (pyenv.Env, error)

	// OnReady is called after each generation completes its handshake, with the
	// generation number. This is where plugins are loaded: a host with none
	// loaded is running but useless, and a restart is only finished once they
	// are back.
	//
	// It runs on the supervisor's own goroutine, so it must not block for long.
	OnReady func(generation int)

	// OnGenerationEnd is called when a generation stops, whatever the reason.
	// In-flight work belonging to it has to be failed here, or the UI waits
	// forever on a process that no longer exists.
	OnGenerationEnd func(generation int)

	// HostScript overrides the host entry point. Nil-valued (empty) uses the
	// extracted runtime.
	HostScript string

	// Rand supplies backoff jitter; nil uses the default source.
	Rand func() float64
}

// Conn is the supervisor's view of a live connection to the host.
//
// The transport lives elsewhere; the supervisor only needs to verify liveness
// and close the connection, which keeps the two independently testable.
type Conn interface {
	// Ping verifies the host is responsive.
	Ping(ctx context.Context) error
	// Shutdown asks the host to exit cleanly.
	Shutdown(ctx context.Context, grace time.Duration) error
	// Close releases the connection.
	Close() error
}

// Status is the supervisor's externally visible state.
type Status struct {
	State proc.State `json:"state"`
	// Message explains the current state in one line.
	Message string `json:"message,omitempty"`
	// Remediation is set when the user can fix the problem.
	Remediation string `json:"remediation,omitempty"`

	Generation int `json:"generation"`
	PID        int `json:"pid,omitempty"`

	// Restarts counts crashes within the current window.
	Restarts int `json:"restarts"`
	// NextRestart is when a backoff expires.
	NextRestart *time.Time `json:"next_restart,omitempty"`

	// Environment is the last environment probe.
	Environment pyenv.Report `json:"environment"`
	// LastCrash describes the most recent failure, if any.
	LastCrash *CrashReport `json:"last_crash,omitempty"`
}

// Supervisor owns the plugin host process.
//
// One goroutine owns all state transitions; everything else communicates by
// channel. That removes any question of lock ordering between the health
// checker, the process reaper and API-driven restarts.
type Supervisor struct {
	cfg Config

	mu     sync.RWMutex
	status Status
	conn   Conn
	proc   *proc.Process

	// address and token belong to the current generation. They are not part of
	// Status: the token is a secret and must not reach a JSON response.
	address string
	token   string

	stderr      *proc.RingBuffer
	backoff     proc.BackoffPolicy
	crashWindow proc.CrashWindow
	history     []CrashReport

	// runningPlugin names what is executing, for crash attribution.
	runningPlugin string

	started  bool
	stopCh   chan struct{}
	restart  chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once
}

// New builds a supervisor.
func New(cfg Config) *Supervisor {
	return &Supervisor{
		cfg:         cfg,
		stderr:      proc.NewRingBuffer(200),
		backoff:     proc.DefaultBackoffPolicy(),
		crashWindow: proc.CrashWindow{Limit: 10, Window: 10 * time.Minute},
		stopCh:      make(chan struct{}),
		restart:     make(chan struct{}, 1),
		doneCh:      make(chan struct{}),
		status:      Status{State: proc.StateStopped},
	}
}

// Start begins supervision. Idempotent.
func (s *Supervisor) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	go s.run()
}

// Stop shuts the host down and ends supervision.
func (s *Supervisor) Stop(ctx context.Context) {
	s.stopOnce.Do(func() { close(s.stopCh) })

	select {
	case <-s.doneCh:
	case <-ctx.Done():
	}
}

// Restart asks for the host to be recycled.
//
// This is the normal way to apply a plugin change: the host holds no durable
// state, so discarding it is both cheap and always correct.
func (s *Supervisor) Restart() {
	select {
	case s.restart <- struct{}{}:
	default:
		// A restart is already pending; one is enough.
	}
}

// Status returns the current state.
func (s *Supervisor) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Conn returns the live connection, or nil when the host is not ready.
func (s *Supervisor) Conn() Conn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status.State != proc.StateReady {
		return nil
	}
	return s.conn
}

// Address returns the transport address of the running host, or "".
func (s *Supervisor) Address() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status.State != proc.StateReady {
		return ""
	}
	return s.address
}

// Token returns the current generation's shared secret, or "".
//
// Deliberately not part of Status, which is serialised to the API: this value
// authorises Bridge access and must never appear in a response.
func (s *Supervisor) Token() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.status.State != proc.StateReady {
		return ""
	}
	return s.token
}

// SetRunningPlugin records what is executing, so a crash can be attributed.
func (s *Supervisor) SetRunningPlugin(name string) {
	s.mu.Lock()
	s.runningPlugin = name
	s.mu.Unlock()
}

// CrashHistory returns recent crash reports, newest first.
func (s *Supervisor) CrashHistory() []CrashReport {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]CrashReport, len(s.history))
	for i, report := range s.history {
		out[len(s.history)-1-i] = report
	}
	return out
}

// run is the single state-owning goroutine.
func (s *Supervisor) run() {
	defer close(s.doneCh)

	if !s.cfg.Enabled {
		s.setState(proc.StateDisabled, "Plugin hosting is disabled.", "")
		<-s.stopCh
		return
	}

	env, ok := s.prepareEnvironment()
	if !ok {
		// Unavailable is a resting state: Stash runs normally, and the UI can
		// tell the user exactly what to install. Wait for a restart request,
		// since installing Python is something they can do without a reboot.
		for {
			select {
			case <-s.stopCh:
				return
			case <-s.restart:
				if e, ok := s.prepareEnvironment(); ok {
					env = e
					goto supervise
				}
			}
		}
	}

supervise:
	// The host runtime is extracted alongside the binary. If it is absent there
	// is nothing to supervise, and retrying forever would bury that fact under
	// a crash loop - so report it as a configuration problem and rest.
	if script, ok := s.hostScript(); !ok {
		s.setState(proc.StateUnavailable,
			"The plugin host runtime is not installed.",
			"Reinstall Stash, or disable AI plugin hosting.")
		logger.Warnf("AI plugin host runtime missing at %s; plugin hosting is unavailable", script)
		for {
			select {
			case <-s.stopCh:
				return
			case <-s.restart:
				if _, ok := s.hostScript(); ok {
					goto supervise
				}
			}
		}
	}

	attempt := 0
	for {
		select {
		case <-s.stopCh:
			s.shutdownProcess()
			return
		default:
		}

		if err := s.startGeneration(env); err != nil {
			logger.Errorf("AI plugin host failed to start: %v", err)
			s.recordCrash(-1, err.Error())

			if s.tooManyCrashes() {
				s.setState(proc.StateFailed,
					fmt.Sprintf("The plugin host failed %d times; not retrying.", s.crashWindow.Limit),
					"Check the crash log, then restart the host from Stash's AI settings.")
				// Only a manual restart leaves StateFailed.
				select {
				case <-s.stopCh:
					return
				case <-s.restart:
					attempt = 0
					s.clearCrashes()
					continue
				}
			}

			delay := s.backoff.Delay(attempt, s.randFunc())
			attempt++
			next := time.Now().Add(delay)
			s.setBackoff(next)

			select {
			case <-s.stopCh:
				return
			case <-s.restart:
				attempt = 0
			case <-time.After(delay):
			}
			continue
		}

		// A generation that reached ready resets the backoff: the next failure
		// should retry quickly rather than inherit an old delay.
		attempt = 0

		generation := s.Status().Generation
		if s.cfg.OnReady != nil {
			s.cfg.OnReady(generation)
		}

		reason := s.superviseGeneration()
		s.shutdownProcess()

		if s.cfg.OnGenerationEnd != nil {
			s.cfg.OnGenerationEnd(generation)
		}

		switch reason {
		case reasonStopped:
			return
		case reasonRestartRequested:
			continue
		case reasonCrashed:
			if s.tooManyCrashes() {
				s.setState(proc.StateFailed,
					fmt.Sprintf("The plugin host crashed %d times in %s; not retrying.",
						s.crashWindow.Limit, s.crashWindow.Window),
					"Check the crash log, then restart the host from Stash's AI settings.")
				select {
				case <-s.stopCh:
					return
				case <-s.restart:
					s.clearCrashes()
				}
				continue
			}
			delay := s.backoff.Delay(attempt, s.randFunc())
			attempt++
			s.setBackoff(time.Now().Add(delay))
			select {
			case <-s.stopCh:
				return
			case <-s.restart:
				attempt = 0
			case <-time.After(delay):
			}
		}
	}
}

// hostModule is the package the extracted runtime is launched as.
const hostModule = "stash_ai_host"

// hostScript returns the host entry point and whether it exists.
//
// The runtime runs as a module, but a module's presence is still checked by
// looking for its file: an absent runtime is a configuration state to report,
// not an import error to discover after spawning.
func (s *Supervisor) hostScript() (string, bool) {
	script := s.cfg.HostScript
	if script == "" {
		script = filepath.Join(s.cfg.BaseDir, "runtime", hostModule, "__main__.py")
	}
	_, err := os.Stat(script)
	return script, err == nil
}

// prepareEnvironment probes and builds the Python environment.
func (s *Supervisor) prepareEnvironment() (pyenv.Env, bool) {
	s.setState(proc.StateProbing, "Checking the Python environment.", "")

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	prepare := s.cfg.PrepareEnv
	if prepare == nil {
		manager := pyenv.Manager{
			VenvDir:          filepath.Join(s.cfg.BaseDir, "venv"),
			ConfiguredPython: s.cfg.ConfiguredPython,
		}
		prepare = manager.Ensure
	}

	env, err := prepare(ctx)

	s.mu.Lock()
	s.status.Environment = env.Report
	s.mu.Unlock()

	if err != nil {
		s.setState(proc.StateUnavailable, env.Report.Message, env.Report.Remediation)
		logger.Warnf("AI plugin host unavailable: %s", env.Report.Message)
		return pyenv.Env{}, false
	}
	return env, true
}

// startGeneration spawns a host and completes the handshake.
func (s *Supervisor) startGeneration(env pyenv.Env) error {
	s.mu.Lock()
	s.status.Generation++
	generation := s.status.Generation
	s.mu.Unlock()

	s.setState(proc.StateStarting, "Starting the plugin host.", "")

	token, err := proc.GenerateToken()
	if err != nil {
		return err
	}

	address, _, err := socketAddress(filepath.Join(s.cfg.BaseDir, "run"))
	if err != nil {
		return err
	}

	runtimeDir := filepath.Join(s.cfg.BaseDir, "runtime")
	cfg := spawnConfig{
		Interpreter: env.Interpreter,
		Script:      s.cfg.HostScript,
		Address:     address,
		Token:       token,
		PluginDir:   s.cfg.PluginDir,
		RuntimeDir:  runtimeDir,
		LogLevel:    s.cfg.LogLevel,
	}
	if cfg.Script == "" {
		cfg.Module = hostModule
	}

	ctx, cancel := context.WithTimeout(context.Background(), readyTimeout+10*time.Second)
	defer cancel()

	process, info, err := spawn(ctx, cfg, s.stderr)
	if err != nil {
		return err
	}

	s.mu.Lock()
	s.proc = process
	s.status.PID = process.PID()
	s.mu.Unlock()

	s.setState(proc.StateHandshaking, "Verifying the plugin host.", "")

	if s.cfg.Connect == nil {
		// Without a transport there is nothing to verify; useful in tests and
		// harmless in production, where Connect is always supplied.
		s.setState(proc.StateReady, "Plugin host ready.", "")
		return nil
	}

	connectCtx, connectCancel := context.WithTimeout(context.Background(), readyTimeout)
	defer connectCancel()

	conn, err := s.cfg.Connect(connectCtx, info.Address, token)
	if err != nil {
		_ = process.Stop(stopGrace)
		return fmt.Errorf("handshake with generation %d failed: %w", generation, err)
	}

	s.mu.Lock()
	s.conn = conn
	// The host reports the address peers should dial, which is not always the
	// one it was asked to listen on. Kept so a second connection can be opened
	// without re-deriving it.
	s.address = info.Address
	s.token = token
	s.mu.Unlock()

	s.setState(proc.StateReady,
		fmt.Sprintf("Plugin host ready (python %s, gi %s).", info.Python, info.GI), "")
	logger.Infof("AI plugin host ready: generation %d, pid %d", generation, process.PID())
	return nil
}

// exitReason says why a generation ended.
type exitReason int

const (
	reasonStopped exitReason = iota
	reasonRestartRequested
	reasonCrashed
)

// superviseGeneration health-checks a running host until something ends it.
func (s *Supervisor) superviseGeneration() exitReason {
	s.mu.RLock()
	process, conn := s.proc, s.conn
	s.mu.RUnlock()

	if process == nil {
		return reasonCrashed
	}

	ticker := time.NewTicker(pingInterval)
	defer ticker.Stop()

	missed := 0

	for {
		select {
		case <-s.stopCh:
			return reasonStopped

		case <-s.restart:
			return reasonRestartRequested

		case <-process.Exited():
			// The process died on its own; that is a crash regardless of code.
			code := process.ExitCode()
			s.recordCrash(code, fmt.Sprintf("host exited with code %d", code))
			return reasonCrashed

		case <-ticker.C:
			if conn == nil {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
			err := conn.Ping(ctx)
			cancel()

			if err == nil {
				missed = 0
				continue
			}

			missed++
			logger.Debugf("AI plugin host missed ping %d/%d: %v", missed, missedPings, err)
			if missed >= missedPings {
				// Unresponsive but not exited: a wedged host is as useless as a
				// dead one, and worse because it holds the socket.
				s.recordCrash(-1, fmt.Sprintf("host stopped responding: %v", err))
				return reasonCrashed
			}
		}
	}
}

// shutdownProcess ends the current generation.
func (s *Supervisor) shutdownProcess() {
	s.mu.Lock()
	conn, process := s.conn, s.proc
	s.conn, s.proc = nil, nil
	s.address, s.token = "", ""
	s.mu.Unlock()

	if conn != nil {
		s.setState(proc.StateDraining, "Finishing in-flight plugin work.", "")
		ctx, cancel := context.WithTimeout(context.Background(), drainTimeout)
		_ = conn.Shutdown(ctx, drainTimeout)
		cancel()
		_ = conn.Close()
	}

	if process != nil {
		s.setState(proc.StateStopping, "Stopping the plugin host.", "")
		_ = process.Stop(stopGrace)
	}

	s.mu.Lock()
	s.status.PID = 0
	s.mu.Unlock()
}

// recordCrash logs a failure and keeps its stderr tail.
func (s *Supervisor) recordCrash(exitCode int, message string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	report := CrashReport{
		CrashReport: proc.CrashReport{
			Generation: s.status.Generation,
			ExitCode:   exitCode,
			Error:      message,
			Stderr:     s.stderr.Snapshot(),
			At:         time.Now(),
		},
		RunningPlugin: s.runningPlugin,
	}

	s.crashWindow.Record(report.At)
	s.history = append(s.history, report)
	// Bounded: only the recent past is diagnostically useful.
	if len(s.history) > 20 {
		s.history = s.history[len(s.history)-20:]
	}

	s.status.LastCrash = &report
	s.status.State = proc.StateCrashed
	s.status.Message = message
	s.status.Restarts = s.countRecentLocked()

	logger.Errorf("AI plugin host crashed (generation %d, exit %d): %s",
		report.Generation, exitCode, message)
}

// tooManyCrashes reports whether the restart cap has been reached.
func (s *Supervisor) tooManyCrashes() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.countRecentLocked() >= s.crashWindow.Limit
}

// countRecentLocked counts crashes inside the window, pruning older ones.
func (s *Supervisor) countRecentLocked() int {
	return s.crashWindow.Count(time.Now())
}

func (s *Supervisor) clearCrashes() {
	s.mu.Lock()
	s.crashWindow.Reset()
	s.status.Restarts = 0
	s.mu.Unlock()
}

func (s *Supervisor) setState(state proc.State, message, remediation string) {
	s.mu.Lock()
	s.status.State = state
	s.status.Message = message
	s.status.Remediation = remediation
	if state != proc.StateBackoff {
		s.status.NextRestart = nil
	}
	s.mu.Unlock()
}

func (s *Supervisor) setBackoff(next time.Time) {
	s.mu.Lock()
	s.status.State = proc.StateBackoff
	s.status.Message = fmt.Sprintf("Restarting the plugin host at %s.", next.Format(time.TimeOnly))
	s.status.NextRestart = &next
	s.status.Restarts = s.countRecentLocked()
	s.mu.Unlock()
}

func (s *Supervisor) randFunc() func() float64 {
	if s.cfg.Rand != nil {
		return s.cfg.Rand
	}
	return rand.Float64
}
