package python

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedUVReleaseMatrix(t *testing.T) {
	tests := []struct {
		goos, goarch string
		musl         bool
		archive      string
	}{
		{"windows", "amd64", false, "uv-x86_64-pc-windows-msvc.zip"},
		{"darwin", "amd64", false, "uv-x86_64-apple-darwin.tar.gz"},
		{"darwin", "arm64", false, "uv-aarch64-apple-darwin.tar.gz"},
		{"linux", "amd64", false, "uv-x86_64-unknown-linux-gnu.tar.gz"},
		{"linux", "amd64", true, "uv-x86_64-unknown-linux-musl.tar.gz"},
		{"linux", "arm64", false, "uv-aarch64-unknown-linux-gnu.tar.gz"},
		{"linux", "arm64", true, "uv-aarch64-unknown-linux-musl.tar.gz"},
		{"linux", "arm", false, "uv-armv7-unknown-linux-gnueabihf.tar.gz"},
		{"linux", "arm", true, "uv-armv7-unknown-linux-musleabihf.tar.gz"},
	}
	for _, test := range tests {
		t.Run(test.goos+"/"+test.goarch+"/"+test.archive, func(t *testing.T) {
			assets, err := currentReleaseAssets(test.goos, test.goarch, test.musl)
			require.NoError(t, err)
			assert.Equal(t, test.archive, assets[0].Archive)
			assert.Len(t, assets[0].SHA256, 64)
		})
	}
	_, err := currentReleaseAssets("freebsd", "amd64", false)
	assert.EqualError(t, err, "uv 0.11.33 is not available for this platform")
}

func TestValidateIndexes(t *testing.T) {
	normalized, err := ValidateIndexes([]Index{
		{Name: "Internal", URL: "HTTPS://EXAMPLE.COM/simple", Default: true},
		{Name: "mirror-2", URL: "http://localhost:8080/packages///"},
	})
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/simple/", normalized[0].URL)
	assert.Equal(t, "http://localhost:8080/packages/", normalized[1].URL)

	invalid := [][]Index{
		{},
		{{Name: "pypi", URL: "https://pypi.org/simple/"}},
		{{Name: "pypi", URL: "https://pypi.org/simple/", Default: true}, {Name: "other", URL: "https://other/simple/", Default: true}},
		{{Name: "bad name", URL: "https://pypi.org/simple/", Default: true}},
		{{Name: "pypi", URL: "https://user:secret@pypi.org/simple/", Default: true}},
		{{Name: "pypi", URL: "https://pypi.org/simple/?token=secret", Default: true}},
		{{Name: "One", URL: "https://one/simple/", Default: true}, {Name: "one", URL: "https://two/simple/"}},
	}
	for _, value := range invalid {
		_, err := ValidateIndexes(value)
		assert.Error(t, err)
	}
}

func TestValidatePackageNames(t *testing.T) {
	valid, err := ValidatePackageNames([]string{"Requests", "typing_extensions"})
	require.NoError(t, err)
	assert.Equal(t, []string{"Requests", "typing_extensions"}, valid)
	for _, names := range [][]string{{}, {"requests>=2"}, {"https://example.com/x.whl"}, {"a", "A"}} {
		_, err := ValidatePackageNames(names)
		assert.Error(t, err)
	}
}

func TestExecutionLeaseWriterFairnessAndCancellation(t *testing.T) {
	firstRelease, err := AcquireExecution(context.Background())
	require.NoError(t, err)
	order := make(chan string, 2)
	var releases sync.WaitGroup
	releases.Add(2)
	go func() {
		release, acquireErr := AcquireMutation(context.Background())
		require.NoError(t, acquireErr)
		order <- "writer"
		release()
		release()
		releases.Done()
	}()
	time.Sleep(10 * time.Millisecond)
	go func() {
		release, acquireErr := AcquireExecution(context.Background())
		require.NoError(t, acquireErr)
		order <- "reader"
		release()
		releases.Done()
	}()
	firstRelease()
	assert.Equal(t, "writer", <-order)
	assert.Equal(t, "reader", <-order)
	releases.Wait()

	hold, err := AcquireMutation(context.Background())
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = AcquireExecution(ctx)
	assert.ErrorIs(t, err, context.Canceled)
	hold()
}

func TestListRuntimesCollapsesAliasesAndAddsExternal(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	require.NoError(t, manager.ensureDirectories())
	require.NoError(t, os.WriteFile(manager.uvPath, []byte("uv"), 0755))
	target := filepath.Join(root, "system-python")
	alias := filepath.Join(root, "python3")
	external := filepath.Join(root, "custom-python")
	for _, path := range []string{target, external} {
		require.NoError(t, os.WriteFile(path, []byte("python"), 0755))
	}
	require.NoError(t, os.Symlink(target, alias))
	records := []uvRuntime{
		{Key: "cpython-3.12-linux-x86_64-gnu", Version: "3.12.4", Path: &target, Variant: "default", Implementation: "cpython", Arch: "x86_64"},
		{Key: "cpython-3.12-linux-x86_64-gnu", Version: "3.12.4", Path: &alias, Variant: "default", Implementation: "cpython", Arch: "x86_64"},
		{Key: "cpython-3.13-linux-x86_64-gnu", Version: "3.13.1", Variant: "default", Implementation: "cpython", Arch: "x86_64"},
	}
	payload, err := json.Marshal(records)
	require.NoError(t, err)
	manager.run = func(_ context.Context, name string, args []string, _ []string) (commandResult, error) {
		if name == manager.uvPath && len(args) == 1 && args[0] == "--version" {
			return commandResult{stdout: "uv " + ManagedUVVersion}, nil
		}
		if name == manager.uvPath {
			return commandResult{stdout: string(payload)}, nil
		}
		if name == external {
			return commandResult{stdout: `{"version":"3.11.9","implementation":"cpython","architecture":"x86_64","variant":"default"}`}, nil
		}
		return commandResult{}, errors.New("unexpected command")
	}
	runtimes, err := manager.ListRuntimes(context.Background(), false, "", external)
	require.NoError(t, err)
	assert.Len(t, runtimes, 3)
	sources := make(map[RuntimeSource]int)
	for _, runtime := range runtimes {
		sources[runtime.Source]++
	}
	assert.Equal(t, map[RuntimeSource]int{
		RuntimeSourceSystem: 1, RuntimeSourceExternal: 1, RuntimeSourceManaged: 1,
	}, sources)
}

func TestEnsureEnvironmentUsesExplicitTemporaryTarget(t *testing.T) {
	root := t.TempDir()
	manager := NewManager(root)
	require.NoError(t, manager.ensureDirectories())
	require.NoError(t, os.WriteFile(manager.uvPath, []byte("uv"), 0755))
	base := filepath.Join(root, "base-python")
	require.NoError(t, os.WriteFile(base, []byte("python"), 0755))
	var venvArgs []string
	manager.run = func(_ context.Context, name string, args []string, _ []string) (commandResult, error) {
		if name == manager.uvPath && len(args) == 1 && args[0] == "--version" {
			return commandResult{stdout: "uv " + ManagedUVVersion}, nil
		}
		if name == manager.uvPath {
			venvArgs = append([]string(nil), args...)
			position := -1
			for n, arg := range args {
				if arg == "venv" {
					position = n
					break
				}
			}
			require.GreaterOrEqual(t, position, 0)
			environmentPath := args[position+1]
			require.NoError(t, os.MkdirAll(filepath.Dir(EnvironmentExecutable(environmentPath)), 0755))
			require.NoError(t, os.WriteFile(EnvironmentExecutable(environmentPath), []byte("python"), 0755))
			return commandResult{}, nil
		}
		prefix := filepath.Dir(filepath.Dir(name))
		health, marshalErr := json.Marshal(map[string]string{"executable": name, "prefix": prefix, "version": "3.12.9"})
		require.NoError(t, marshalErr)
		return commandResult{stdout: string(health)}, nil
	}
	selected := Runtime{ID: "system:test", Key: "cpython-3.12", Version: "3.12.4", Installed: true, Path: &base}
	executable, err := manager.EnsureEnvironment(context.Background(), selected)
	require.NoError(t, err)
	assert.Equal(t, EnvironmentExecutable(manager.EnvironmentPath(selected.ID)), executable)
	joined := strings.Join(venvArgs, " ")
	assert.Contains(t, joined, "venv ")
	assert.Contains(t, joined, "--python "+base)
	assert.Contains(t, joined, "--no-python-downloads")
	assert.Contains(t, joined, "--no-config")
	require.NoError(t, manager.CheckEnvironment(context.Background(), selected.ID, executable))
}

func TestCatalogParsersAndPackagePhases(t *testing.T) {
	jsonProjects, err := parsePEP691(strings.NewReader(`{"meta":{"api-version":"1.0"},"projects":[{"name":"Typing_Extensions"},{"name":"requests"}]}`))
	require.NoError(t, err)
	assert.Equal(t, "Typing_Extensions", jsonProjects["typing-extensions"])
	htmlProjects, err := parsePEP503(strings.NewReader(`<html><a href="/simple/requests/">requests</a><a href="/simple/urllib3/">urllib3</a></html>`))
	require.NoError(t, err)
	assert.Equal(t, "urllib3", htmlProjects["urllib3"])

	var events []Event
	reportPackagePhases("Resolved 3 packages\nPrepared 2 packages\nInstalled 2 packages", func(event Event) {
		events = append(events, event)
	})
	assert.Equal(t, []Phase{PhasePreparing, PhaseInstalling, PhaseValidating}, []Phase{events[0].Phase, events[1].Phase, events[2].Phase})
}

func TestWriteCommandConfigAndEnvironmentPrecedence(t *testing.T) {
	manager := NewManager(t.TempDir())
	t.Setenv("UV_INDEX_URL", "https://ambient.invalid/simple")
	for _, item := range manager.environment() {
		assert.False(t, strings.HasPrefix(item, "UV_INDEX_URL="))
	}
	path, cleanup, err := manager.WriteCommandConfig([]Index{{Name: "pypi", URL: "https://pypi.org/simple", Default: true}})
	require.NoError(t, err)
	defer cleanup()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer file.Close()
	contents, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Contains(t, string(contents), `[[index]]`)
	assert.Contains(t, string(contents), `url = 'https://pypi.org/simple/'`)
	info, err := file.Stat()
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
}
