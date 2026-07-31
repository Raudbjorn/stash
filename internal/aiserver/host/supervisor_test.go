package host

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stashapp/stash/internal/aiserver/proc"
	"github.com/stashapp/stash/internal/aiserver/pyenv"
)


// A disabled supervisor must start nothing and rest quietly, so a Stash with
// the feature off behaves exactly as it did before.
func TestDisabledSupervisorStartsNothing(t *testing.T) {
	s := New(Config{Enabled: false})
	s.Start()
	defer stopSupervisor(t, s)

	waitForState(t, s, proc.StateDisabled)

	if s.Conn() != nil {
		t.Error("a disabled supervisor exposed a connection")
	}
	if status := s.Status(); status.PID != 0 {
		t.Errorf("a disabled supervisor reported pid %d", status.PID)
	}
}

// Without a usable Python the supervisor must settle into Unavailable with an
// actionable message, not crash and not spin.
//
// The environment probe is injected rather than driven by a bad python path:
// Stash's python.Resolve deliberately FALLS BACK to PATH when the configured
// path is unusable, so a bad setting does not produce an unusable environment.
func TestUnavailableEnvironmentIsARestingState(t *testing.T) {
	s := New(Config{
		Enabled: true,
		BaseDir: t.TempDir(),
		PrepareEnv: func(context.Context) (pyenv.Env, error) {
			return pyenv.Env{Report: pyenv.Report{
					Status:      pyenv.StatusPyGObjectMissing,
					Message:     "PyGObject is not available to this interpreter.",
					Remediation: "Install python3-gi.",
				}},
				errors.New("python environment unusable")
		},
	})
	s.Start()
	defer stopSupervisor(t, s)

	waitForState(t, s, proc.StateUnavailable)

	status := s.Status()
	if status.Message == "" {
		t.Error("Unavailable carried no explanation")
	}
	if status.Remediation == "" {
		t.Error("Unavailable carried no remediation; the UI has nothing to show the user")
	}
	if s.Conn() != nil {
		t.Error("an unavailable supervisor exposed a connection")
	}

	// It must stay put rather than retrying in a loop.
	time.Sleep(300 * time.Millisecond)
	if got := s.Status().State; got != proc.StateUnavailable {
		t.Errorf("state drifted to %q; Unavailable should be a resting state", got)
	}
}

// echoConfig supervises the echo host: a real Python child that honours the
// startup contract, so the lifecycle is exercised end to end without needing
// the transport or any plugin code.
func echoConfig(t *testing.T, behave string) Config {
	t.Helper()

	python := requirePython(t)
	t.Setenv("ECHO_BEHAVE", behave)

	// Short base path: unix socket paths are capped near 108 bytes.
	dir, err := os.MkdirTemp("", "aisup")
	if err != nil {
		t.Fatalf("tempdir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	return Config{
		Enabled:    true,
		BaseDir:    dir,
		HostScript: echoScript(t),
		PrepareEnv: func(context.Context) (pyenv.Env, error) {
			return pyenv.Env{Interpreter: python, Report: pyenv.Report{Status: pyenv.StatusOK}}, nil
		},
		// No jitter, so backoff timing in tests is predictable.
		Rand: func() float64 { return 0.5 },
	}
}

// The happy path: spawn, handshake, reach ready.
func TestSupervisorReachesReady(t *testing.T) {
	s := New(echoConfig(t, "serve"))
	s.Start()
	defer stopSupervisor(t, s)

	waitForState(t, s, proc.StateReady)

	status := s.Status()
	if status.PID == 0 {
		t.Error("ready state reported no pid")
	}
	if status.Generation != 1 {
		t.Errorf("generation = %d, want 1", status.Generation)
	}
}

// A host that dies must be noticed, reported with its stderr, and restarted.
func TestSupervisorRestartsAfterCrash(t *testing.T) {
	s := New(echoConfig(t, "crash-after"))
	s.Start()
	defer stopSupervisor(t, s)

	// It reports ready, then exits; the supervisor must observe the exit and
	// come back round rather than sitting on a dead process.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status().Generation >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	status := s.Status()
	if status.Generation < 2 {
		t.Fatalf("the host was not restarted after crashing: %+v", status)
	}
	if status.LastCrash == nil {
		t.Fatal("no crash was recorded")
	}
	if status.LastCrash.ExitCode != 23 {
		t.Errorf("exit code = %d, want 23 from the echo host", status.LastCrash.ExitCode)
	}
	if !containsLine(status.LastCrash.Stderr, "exiting after reporting ready") {
		t.Errorf("the crash report lost the child's stderr: %v", status.LastCrash.Stderr)
	}
}

// The property the plan calls for: kill the host repeatedly and confirm Stash
// stays up, each death is recorded, and the supervisor keeps its accounting
// straight rather than leaking processes or wedging.
func TestSupervisorSurvivesRepeatedKills(t *testing.T) {
	s := New(echoConfig(t, "serve"))
	s.Start()
	defer stopSupervisor(t, s)

	waitForState(t, s, proc.StateReady)

	const kills = 3
	for i := range kills {
		s.mu.RLock()
		process := s.proc
		s.mu.RUnlock()
		if process == nil {
			t.Fatalf("iteration %d: no process to kill", i)
		}

		before := s.Status().Generation
		if err := process.Kill(); err != nil {
			t.Fatalf("iteration %d: kill: %v", i, err)
		}

		// It must come back on its own.
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			st := s.Status()
			if st.Generation > before && st.State == proc.StateReady {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}

		st := s.Status()
		if st.Generation <= before {
			t.Fatalf("iteration %d: no restart (generation stuck at %d, state %q)",
				i, st.Generation, st.State)
		}
	}

	final := s.Status()
	if final.State != proc.StateReady {
		t.Errorf("final state = %q, want ready after recovering", final.State)
	}
	if got := len(s.CrashHistory()); got < kills {
		t.Errorf("recorded %d crashes, want at least %d", got, kills)
	}
	// Every kill must be attributed to its own generation.
	if final.Generation < kills+1 {
		t.Errorf("generation = %d, want at least %d", final.Generation, kills+1)
	}
}

// A host that never reports ready fails the generation rather than hanging
// supervision forever.
func TestSupervisorHandlesUnresponsiveHost(t *testing.T) {
	cfg := echoConfig(t, "crash-now")
	s := New(cfg)
	s.Start()
	defer stopSupervisor(t, s)

	// It should crash and enter backoff, not stall.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		switch s.Status().State {
		case proc.StateBackoff, proc.StateCrashed:
			if s.Status().LastCrash != nil {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("supervisor did not report a failure: %+v", s.Status())
}

// Restart recycles the host, which is how a plugin change is applied.
func TestSupervisorRestartRecyclesHost(t *testing.T) {
	s := New(echoConfig(t, "serve"))
	s.Start()
	defer stopSupervisor(t, s)

	waitForState(t, s, proc.StateReady)
	first := s.Status()

	s.Restart()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		st := s.Status()
		if st.Generation > first.Generation && st.State == proc.StateReady {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	second := s.Status()
	if second.Generation <= first.Generation {
		t.Fatalf("restart did not start a new generation: %+v", second)
	}
	if second.PID == first.PID {
		t.Error("restart reused the same process")
	}
	// A deliberate recycle is not a crash.
	if len(s.CrashHistory()) != 0 {
		t.Errorf("a requested restart was recorded as a crash: %v", s.CrashHistory())
	}
}

// Stopping must leave no child behind.
func TestSupervisorStopTerminatesChild(t *testing.T) {
	s := New(echoConfig(t, "serve"))
	s.Start()

	waitForState(t, s, proc.StateReady)

	s.mu.RLock()
	process := s.proc
	s.mu.RUnlock()
	if process == nil {
		t.Fatal("no process")
	}

	s.Stop(timeoutContext(t, 20*time.Second))

	select {
	case <-process.Exited():
	case <-time.After(10 * time.Second):
		t.Fatal("the child outlived the supervisor")
	}
}

// The restart cap: a host that keeps failing must eventually stop being
// retried, so the evidence is preserved rather than buried under restarts.
func TestCrashAccountingReachesFailed(t *testing.T) {
	s := New(Config{Enabled: true, BaseDir: t.TempDir()})

	for range s.crashWindow.Limit {
		s.recordCrash(1, "simulated failure")
	}

	if !s.tooManyCrashes() {
		t.Fatalf("%d crashes should reach the cap", s.crashWindow.Limit)
	}

	history := s.CrashHistory()
	if len(history) == 0 {
		t.Fatal("no crash history recorded")
	}
	// Newest first, so the UI shows the most relevant failure at the top.
	if history[0].Error != "simulated failure" {
		t.Errorf("history[0] = %+v", history[0])
	}

	// A manual restart clears the count, which is what makes Failed
	// recoverable without restarting Stash.
	s.clearCrashes()
	if s.tooManyCrashes() {
		t.Error("clearing crashes did not reset the cap")
	}
}


// Crash reports must carry the stderr tail, which is the only place a Python
// traceback survives the process.
func TestCrashReportCarriesStderr(t *testing.T) {
	s := New(Config{Enabled: true, BaseDir: t.TempDir()})
	s.stderr.Add("Traceback (most recent call last):")
	s.stderr.Add("RuntimeError: boom")
	s.SetRunningPlugin("skier_aitagging")

	s.recordCrash(9, "host exited with code 9")

	report := s.Status().LastCrash
	if report == nil {
		t.Fatal("no crash report")
	}
	if report.ExitCode != 9 {
		t.Errorf("exit code = %d", report.ExitCode)
	}
	if !containsLine(report.Stderr, "RuntimeError: boom") {
		t.Errorf("stderr not captured: %v", report.Stderr)
	}
	// Attribution matters: if one plugin dominates the crash history, that is
	// what to tell the user.
	if report.RunningPlugin != "skier_aitagging" {
		t.Errorf("running plugin = %q", report.RunningPlugin)
	}
}

// Stop must be safe whatever state the supervisor is in, including never
// started.
func TestStopIsSafeBeforeStart(t *testing.T) {
	s := New(Config{Enabled: true, BaseDir: t.TempDir()})
	// Not started; Stop must not block on a goroutine that never ran.
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Stop(timeoutContext(t, time.Second))
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop blocked on a supervisor that was never started")
	}
}

func TestRestartRequestDoesNotBlock(t *testing.T) {
	s := New(Config{Enabled: false})

	// The channel is depth 1; extra requests coalesce rather than blocking a
	// caller holding a lock somewhere else.
	for range 10 {
		s.Restart()
	}
}

// ------------------------------------------------------------------ utils ---

func waitForState(t *testing.T, s *Supervisor, want proc.State) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if s.Status().State == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %q; current: %+v", want, s.Status())
}

func stopSupervisor(t *testing.T, s *Supervisor) {
	t.Helper()
	s.Stop(timeoutContext(t, 10*time.Second))
}

func timeoutContext(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}
