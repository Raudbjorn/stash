package python

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type commandResult struct {
	stdout string
	stderr string
}

type commandRunner func(context.Context, string, []string, []string) (commandResult, error)

type Manager struct {
	root             string
	binDir           string
	pythonsDir       string
	environmentsDir  string
	cacheDir         string
	catalogsDir      string
	uvPath           string
	httpClient       *http.Client
	run              commandRunner
	now              func() time.Time
	platform         func() (string, string, bool)
	lookPath         func(string) (string, error)
	engineMu         sync.RWMutex
	mutationMu       sync.Mutex
	catalogMu        sync.Mutex
	catalogRefreshMu sync.Mutex
	catalogDB        *sql.DB
}

func NewManager(root string) *Manager {
	binDir := filepath.Join(root, "bin")
	uvName := "uv"
	if goos, _, _ := hostPlatform(); goos == "windows" {
		uvName += ".exe"
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
	client.CheckRedirect = safeRedirects
	return &Manager{
		root:            root,
		binDir:          binDir,
		pythonsDir:      filepath.Join(root, "pythons"),
		environmentsDir: filepath.Join(root, "environments"),
		cacheDir:        filepath.Join(root, "cache"),
		catalogsDir:     filepath.Join(root, "catalogs"),
		uvPath:          filepath.Join(binDir, uvName),
		httpClient:      client,
		run:             defaultCommandRunner,
		now:             time.Now,
		platform:        hostPlatform,
		lookPath:        exec.LookPath,
	}
}

func (m *Manager) Close() error {
	m.catalogMu.Lock()
	defer m.catalogMu.Unlock()
	if m.catalogDB == nil {
		return nil
	}
	err := m.catalogDB.Close()
	m.catalogDB = nil
	return err
}

func defaultCommandRunner(ctx context.Context, name string, args []string, env []string) (commandResult, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return commandResult{stdout: stdout.String(), stderr: stderr.String()}, err
}

func safeRedirects(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 {
		return errors.New("too many redirects")
	}
	if req.URL.Scheme != "https" {
		return errors.New("redirect target must use HTTPS")
	}
	return nil
}

func (m *Manager) ensureDirectories() error {
	for _, dir := range []string{m.binDir, m.pythonsDir, m.environmentsDir, m.cacheDir, m.catalogsDir} {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("create Python manager directory %q: %w", dir, err)
		}
	}
	return nil
}

var removedEnvironmentKeys = map[string]struct{}{
	"UV_INDEX": {}, "UV_DEFAULT_INDEX": {}, "UV_INDEX_URL": {}, "UV_EXTRA_INDEX_URL": {},
	"UV_FIND_LINKS": {}, "PIP_INDEX_URL": {}, "PIP_EXTRA_INDEX_URL": {}, "PIP_FIND_LINKS": {},
	"UV_PYTHON_INSTALL_MIRROR": {}, "UV_PYPY_INSTALL_MIRROR": {}, "UV_PYTHON_DOWNLOADS_JSON_URL": {},
}

func (m *Manager) environment() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		remove := false
		for blocked := range removedEnvironmentKeys {
			if strings.EqualFold(key, blocked) {
				remove = true
				break
			}
		}
		if !remove {
			env = append(env, item)
		}
	}
	env = append(env, "UV_PYTHON_INSTALL_DIR="+m.pythonsDir, "UV_CACHE_DIR="+m.cacheDir)
	return env
}

func (m *Manager) runUV(ctx context.Context, args []string) (commandResult, error) {
	m.engineMu.RLock()
	defer m.engineMu.RUnlock()
	path, err := m.enginePathLocked(ctx)
	if err != nil {
		return commandResult{}, err
	}
	machineArgs := append([]string{"--no-progress", "--color", "never"}, args...)
	result, err := m.run(ctx, path, machineArgs, m.environment())
	if err != nil {
		message := commandErrorMessage(result.stderr)
		if message == "" {
			message = err.Error()
		}
		return result, fmt.Errorf("uv command failed: %s", message)
	}
	return result, nil
}

func commandErrorMessage(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for n := len(lines) - 1; n >= 0; n-- {
		line := strings.TrimSpace(lines[n])
		if line != "" {
			if len(line) > 512 {
				line = line[:512]
			}
			return line
		}
	}
	return ""
}

func (m *Manager) enginePathLocked(ctx context.Context) (string, error) {
	goos, goarch, musl := m.platform()
	if _, err := currentReleaseAssets(goos, goarch, musl); err == nil {
		if _, err := m.validateEngine(ctx, m.uvPath); err != nil {
			return "", fmt.Errorf("managed uv is unavailable: %w", err)
		}
		return m.uvPath, nil
	}
	path, err := m.lookPath("uv")
	if err != nil {
		return "", fmt.Errorf("uv %s is not available for this platform", ManagedUVVersion)
	}
	if _, err := m.validateEngine(ctx, path); err != nil {
		return "", fmt.Errorf("uv %s is not available for this platform", ManagedUVVersion)
	}
	return path, nil
}

func (m *Manager) validateEngine(ctx context.Context, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", errors.New("uv path is a directory")
	}
	result, err := m.run(ctx, path, []string{"--version"}, m.environment())
	if err != nil {
		return "", err
	}
	fields := strings.Fields(strings.TrimSpace(result.stdout))
	if len(fields) < 2 || fields[0] != "uv" || fields[1] != ManagedUVVersion {
		return "", fmt.Errorf("expected uv %s, got %q", ManagedUVVersion, strings.TrimSpace(result.stdout))
	}
	return fields[1], nil
}

func stringPtr(value string) *string { return &value }
