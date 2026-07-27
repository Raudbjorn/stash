// Package pyenv manages the Python environment the AI plugin host runs in:
// finding a usable interpreter, building an isolated virtual environment, and
// reporting precisely what is missing when it cannot.
//
// The guiding rule is degrade, never crash. Plugin support is optional; a Stash
// installation with no Python must boot exactly as it did before, and the
// reason must be visible in the UI rather than buried in a log.
package pyenv

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	stashExec "github.com/stashapp/stash/pkg/exec"
	"github.com/stashapp/stash/pkg/python"
)

// Status classifies the environment.
type Status string

const (
	// StatusOK means the environment can host plugins.
	StatusOK Status = "ok"
	// StatusPythonMissing means no interpreter was found.
	StatusPythonMissing Status = "python-missing"
	// StatusPythonTooOld means the interpreter is below the minimum.
	StatusPythonTooOld Status = "python-too-old"
	// StatusPyGObjectMissing means PyGObject is unavailable.
	//
	// This is the cost of the D-Bus transport: PyGObject is a compiled system
	// package, trivial on Linux and a genuine obstacle elsewhere.
	StatusPyGObjectMissing Status = "pygobject-missing"
	// StatusVenvBroken means the virtual environment exists but is unusable.
	StatusVenvBroken Status = "venv-broken"
	// StatusProbeFailed means the probe itself could not be run.
	StatusProbeFailed Status = "probe-failed"
)

// minPythonMajor / minPythonMinor is the oldest interpreter the host supports.
const (
	minPythonMajor = 3
	minPythonMinor = 11
)

// probeTimeout bounds the probe. A hung interpreter must not delay startup.
const probeTimeout = 20 * time.Second

// Report is the outcome of probing the environment.
type Report struct {
	Status Status `json:"status"`
	// Message is a one-line summary.
	Message string `json:"message"`
	// Remediation tells the user how to fix it, in their platform's terms.
	Remediation string `json:"remediation,omitempty"`

	Interpreter   string `json:"interpreter,omitempty"`
	PythonVersion string `json:"python_version,omitempty"`
	GIVersion     string `json:"gi_version,omitempty"`
	GLibVersion   string `json:"glib_version,omitempty"`
}

// OK reports whether the environment can host plugins.
func (r Report) OK() bool { return r.Status == StatusOK }

// probeScript reports the interpreter's own view of itself.
//
// It checks PyGObject by importing it rather than by looking for a package,
// because the import is what will actually happen at runtime.
const probeScript = `
import json, sys
out = {"python": "%d.%d.%d" % sys.version_info[:3], "executable": sys.executable}
try:
    import gi
    gi.require_version("Gio", "2.0")
    from gi.repository import Gio, GLib
    out["gi"] = gi.__version__
    out["glib"] = "%d.%d" % (GLib.MAJOR_VERSION, GLib.MINOR_VERSION)
except Exception as exc:
    out["gi_error"] = repr(exc)
print(json.dumps(out))
`

type probeResult struct {
	Python     string `json:"python"`
	Executable string `json:"executable"`
	GI         string `json:"gi"`
	GLib       string `json:"glib"`
	GIError    string `json:"gi_error"`
}

// Doctor probes a Python environment.
type Doctor struct {
	// ConfiguredPython is Stash's python_path setting; empty means search PATH.
	ConfiguredPython string
}

// Probe examines the given interpreter, or resolves one if empty.
func (d Doctor) Probe(ctx context.Context, interpreter string) Report {
	if interpreter == "" {
		resolved, err := python.Resolve(d.ConfiguredPython)
		if err != nil {
			return Report{
				Status:      StatusPythonMissing,
				Message:     "No Python interpreter was found.",
				Remediation: remediationForPython(),
			}
		}
		interpreter = string(*resolved)
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	cmd := stashExec.CommandContext(ctx, interpreter, "-c", probeScript)
	out, err := cmd.Output()
	if err != nil {
		return Report{
			Status:      StatusProbeFailed,
			Interpreter: interpreter,
			Message:     fmt.Sprintf("Could not run %s: %v", interpreter, err),
			Remediation: "Check that the configured Python path is executable.",
		}
	}

	var result probeResult
	if err := json.Unmarshal(trimToJSON(out), &result); err != nil {
		return Report{
			Status:      StatusProbeFailed,
			Interpreter: interpreter,
			Message:     "The Python probe returned unexpected output.",
		}
	}

	report := Report{
		Interpreter:   interpreter,
		PythonVersion: result.Python,
		GIVersion:     result.GI,
		GLibVersion:   result.GLib,
	}

	if !versionAtLeast(result.Python, minPythonMajor, minPythonMinor) {
		report.Status = StatusPythonTooOld
		report.Message = fmt.Sprintf("Python %s is too old; %d.%d or newer is required.",
			result.Python, minPythonMajor, minPythonMinor)
		report.Remediation = remediationForPython()
		return report
	}

	if result.GI == "" {
		report.Status = StatusPyGObjectMissing
		report.Message = "PyGObject is not available to this interpreter."
		report.Remediation = remediationForPyGObject()
		return report
	}

	report.Status = StatusOK
	report.Message = fmt.Sprintf("Python %s with PyGObject %s.", result.Python, result.GI)
	return report
}

// remediationForPyGObject gives platform-specific advice.
//
// PyGObject is a compiled extension needing libgirepository, so pip install is
// not a reliable answer on any platform - hence naming the system package.
func remediationForPyGObject() string {
	switch runtime.GOOS {
	case "linux":
		return "Install your distribution's PyGObject package, for example " +
			"'apt install python3-gi gir1.2-glib-2.0' or 'dnf install python3-gobject' " +
			"or 'pacman -S python-gobject'."
	case "darwin":
		return "Install PyGObject with Homebrew: 'brew install pygobject3 glib'."
	case "windows":
		return "PyGObject on Windows requires MSYS2 or a gvsbuild-produced build; " +
			"pip install alone will not work."
	default:
		return "Install PyGObject for this interpreter."
	}
}

func remediationForPython() string {
	return fmt.Sprintf("Install Python %d.%d or newer, or set the Python path in "+
		"Stash's system settings.", minPythonMajor, minPythonMinor)
}

// versionAtLeast compares a dotted version against a minimum.
func versionAtLeast(v string, major, minor int) bool {
	parts := strings.Split(v, ".")
	if len(parts) < 2 {
		return false
	}
	gotMajor, err1 := atoi(parts[0])
	gotMinor, err2 := atoi(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	if gotMajor != major {
		return gotMajor > major
	}
	return gotMinor >= minor
}

func atoi(s string) (int, error) {
	var n int
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(r-'0')
	}
	return n, nil
}

// trimToJSON isolates the JSON object in output that may carry warnings on
// preceding lines.
func trimToJSON(out []byte) []byte {
	s := strings.TrimSpace(string(out))
	if idx := strings.LastIndex(s, "\n"); idx >= 0 {
		s = strings.TrimSpace(s[idx+1:])
	}
	return []byte(s)
}

// InterpreterPath returns the python executable inside a virtual environment.
func InterpreterPath(venvDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(venvDir, "Scripts", "python.exe")
	}
	return filepath.Join(venvDir, "bin", "python")
}

// Exists reports whether a path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
