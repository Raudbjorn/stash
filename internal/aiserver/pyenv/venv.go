package pyenv

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"

	stashExec "github.com/stashapp/stash/pkg/exec"
	"github.com/stashapp/stash/pkg/logger"
	"github.com/stashapp/stash/pkg/python"
)

// The plugin host runs in its own virtual environment so that installing a
// plugin's dependencies can never disturb the system Python - which on many
// systems is managed by the OS package manager and must not be written to.
//
// The environment is created WITH system site packages. That is not laziness:
// PyGObject is a compiled system package that pip cannot reliably supply, so an
// isolated environment would be unable to import `gi` and the whole D-Bus
// transport would be unavailable.

// venvTimeout bounds environment creation.
const venvTimeout = 5 * time.Minute

// Env describes a prepared plugin environment.
type Env struct {
	// Dir is the virtual environment's root.
	Dir string
	// Interpreter is the python executable inside it.
	Interpreter string
	// Report is the doctor's verdict on that interpreter.
	Report Report
}

// Manager prepares and validates the plugin environment.
type Manager struct {
	// VenvDir is where the environment lives.
	VenvDir string
	// ConfiguredPython is Stash's python_path setting.
	ConfiguredPython string
}

// Ensure returns a usable environment, creating it if necessary.
//
// The base interpreter is probed first: creating an environment from a Python
// that cannot import `gi` would only produce a broken one, and the resulting
// error would point at the environment rather than the real cause.
func (m Manager) Ensure(ctx context.Context) (Env, error) {
	doctor := Doctor{ConfiguredPython: m.ConfiguredPython}

	base := doctor.Probe(ctx, "")
	if !base.OK() {
		return Env{Report: base}, fmt.Errorf("python environment unusable: %s", base.Message)
	}

	interpreter := InterpreterPath(m.VenvDir)
	if !Exists(interpreter) {
		if err := m.create(ctx, base.Interpreter); err != nil {
			return Env{Report: base}, err
		}
	}

	// Probe the environment's own interpreter: creation can succeed while
	// producing something that cannot import what the host needs.
	report := doctor.Probe(ctx, interpreter)
	if !report.OK() {
		report.Status = StatusVenvBroken
		report.Remediation = "Rebuild the plugin environment from Stash's AI settings."
		return Env{Dir: m.VenvDir, Interpreter: interpreter, Report: report},
			fmt.Errorf("plugin environment unusable: %s", report.Message)
	}

	return Env{Dir: m.VenvDir, Interpreter: interpreter, Report: report}, nil
}

// create builds the virtual environment.
func (m Manager) create(ctx context.Context, baseInterpreter string) error {
	if err := os.MkdirAll(m.VenvDir, 0o755); err != nil {
		return fmt.Errorf("create environment directory: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, venvTimeout)
	defer cancel()

	logger.Infof("Creating AI plugin Python environment at %s", m.VenvDir)

	// --system-site-packages is required, not merely convenient: see the note
	// at the top of this file.
	cmd := stashExec.CommandContext(ctx, baseInterpreter,
		"-m", "venv", "--system-site-packages", m.VenvDir)

	if out, err := cmd.CombinedOutput(); err != nil {
		// Leave nothing half-built, or the next attempt would see the
		// directory and assume it is usable.
		_ = os.RemoveAll(m.VenvDir)
		return fmt.Errorf("create virtual environment: %w: %s", err, truncate(string(out), 400))
	}
	return nil
}

// Rebuild discards and recreates the environment. Offered in the UI for when
// an upgrade or a partial install has left it inconsistent.
func (m Manager) Rebuild(ctx context.Context) (Env, error) {
	if err := os.RemoveAll(m.VenvDir); err != nil {
		return Env{}, fmt.Errorf("remove existing environment: %w", err)
	}
	return m.Ensure(ctx)
}

// Command builds a command running inside the environment, with the plugin
// directory on PYTHONPATH.
func (e Env) Command(ctx context.Context, pluginPath string, args ...string) (*exec.Cmd, error) {
	if e.Interpreter == "" {
		return nil, fmt.Errorf("plugin environment is not prepared")
	}

	p := python.New(e.Interpreter)
	cmd := p.Command(ctx, args)
	if pluginPath != "" {
		python.AppendPythonPath(cmd, pluginPath)
	}
	return cmd, nil
}

func truncate(s string, limit int) string {
	s = trimSpace(s)
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && isSpace(s[start]) {
		start++
	}
	for end > start && isSpace(s[end-1]) {
		end--
	}
	return s[start:end]
}

func isSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}
