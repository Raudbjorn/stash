package python

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrRuntimeNotFound     = errors.New("python runtime not found")
	ErrRuntimeNotInstalled = errors.New("python runtime is not installed")
	ErrRuntimeSource       = errors.New("invalid Python runtime source")
)

type uvRuntime struct {
	Key            string  `json:"key"`
	Version        string  `json:"version"`
	Path           *string `json:"path"`
	URL            *string `json:"url"`
	Variant        string  `json:"variant"`
	Implementation string  `json:"implementation"`
	Arch           string  `json:"arch"`
	Libc           *string `json:"libc"`
}

func (m *Manager) ListRuntimes(ctx context.Context, allVersions bool, selectedID, configuredPath string) ([]Runtime, error) {
	args := []string{"python", "list", "--output-format", "json", "--no-config"}
	if allVersions {
		args = append(args, "--all-versions")
	}
	result, err := m.runUV(ctx, args)
	if err != nil {
		return nil, err
	}
	var records []uvRuntime
	decoder := json.NewDecoder(strings.NewReader(result.stdout))
	if err := decoder.Decode(&records); err != nil {
		return nil, fmt.Errorf("decode uv Python runtime list: %w", err)
	}

	managedKeys := make(map[string]int)
	canonicalRows := make(map[string]int)
	ret := make([]Runtime, 0, len(records)+1)
	for _, record := range records {
		if record.Key == "" || record.Version == "" || record.Implementation == "" || record.Arch == "" {
			continue
		}
		runtime := Runtime{
			Key: record.Key, Version: record.Version, Implementation: record.Implementation,
			Architecture: record.Arch, Libc: record.Libc, Variant: record.Variant,
			DownloadURL: record.URL,
		}
		if runtime.Variant == "" {
			runtime.Variant = "default"
		}
		if record.Path == nil || *record.Path == "" {
			runtime.ID = "managed:" + record.Key
			runtime.Source = RuntimeSourceManaged
			if prior, ok := managedKeys[record.Key]; ok {
				if ret[prior].DownloadURL == nil {
					ret[prior].DownloadURL = record.URL
				}
				continue
			}
			managedKeys[record.Key] = len(ret)
			ret = append(ret, runtime)
			continue
		}

		canonical, canonicalErr := canonicalPath(*record.Path)
		if canonicalErr != nil {
			continue
		}
		if prior, ok := canonicalRows[canonical]; ok {
			if ret[prior].DownloadURL == nil {
				ret[prior].DownloadURL = record.URL
			}
			continue
		}
		runtime.Path = &canonical
		runtime.Installed = true
		if pathWithin(canonical, m.pythonsDir) {
			runtime.ID = "managed:" + record.Key
			runtime.Source = RuntimeSourceManaged
			if prior, ok := managedKeys[record.Key]; ok {
				ret[prior] = runtime
				canonicalRows[canonical] = prior
				continue
			}
			managedKeys[record.Key] = len(ret)
		} else {
			runtime.ID = "system:" + pathDigest(canonical)
			runtime.Source = RuntimeSourceSystem
		}
		canonicalRows[canonical] = len(ret)
		ret = append(ret, runtime)
	}

	configuredCanonical := ""
	if configuredPath != "" {
		configuredCanonical, _ = canonicalPath(configuredPath)
	}
	if configuredCanonical != "" {
		if _, found := canonicalRows[configuredCanonical]; !found {
			if external, externalErr := m.externalRuntime(ctx, configuredCanonical); externalErr == nil {
				canonicalRows[configuredCanonical] = len(ret)
				ret = append(ret, external)
			}
		}
	}

	legacyCanonical := configuredCanonical
	if selectedID == "" && legacyCanonical == "" {
		if resolved, resolveErr := Resolve(""); resolveErr == nil {
			legacyCanonical, _ = canonicalPath(string(*resolved))
		}
	}
	for n := range ret {
		ret[n].Selected = (selectedID != "" && ret[n].ID == selectedID) ||
			(selectedID == "" && ret[n].Path != nil && *ret[n].Path == legacyCanonical)
		environmentPath := m.EnvironmentPath(ret[n].ID)
		if info, statErr := os.Stat(environmentPath); statErr == nil && info.IsDir() {
			ret[n].EnvironmentPath = &environmentPath
		}
	}

	sort.SliceStable(ret, func(a, b int) bool {
		if ret[a].Selected != ret[b].Selected {
			return ret[a].Selected
		}
		if ret[a].Installed != ret[b].Installed {
			return ret[a].Installed
		}
		return ret[a].Key > ret[b].Key
	})
	return ret, nil
}

func (m *Manager) externalRuntime(ctx context.Context, path string) (Runtime, error) {
	result, err := m.run(ctx, path, []string{"-c", "import json,platform,sys; print(json.dumps({'version': platform.python_version(), 'implementation': platform.python_implementation().lower(), 'architecture': platform.machine(), 'variant': 'default'}))"}, m.environment())
	if err != nil {
		return Runtime{}, err
	}
	var details struct {
		Version        string `json:"version"`
		Implementation string `json:"implementation"`
		Architecture   string `json:"architecture"`
		Variant        string `json:"variant"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(result.stdout)), &details); err != nil {
		return Runtime{}, err
	}
	return Runtime{
		ID: "external:" + pathDigest(path), Key: filepath.Base(path), Version: details.Version,
		Implementation: details.Implementation, Architecture: details.Architecture, Variant: details.Variant,
		Source: RuntimeSourceExternal, Installed: true, Path: &path,
	}, nil
}

func (m *Manager) InstallRuntime(ctx context.Context, id string) (*Runtime, error) {
	if !strings.HasPrefix(id, "managed:") {
		return nil, ErrRuntimeSource
	}
	runtimes, err := m.ListRuntimes(ctx, true, "", "")
	if err != nil {
		return nil, err
	}
	var candidate *Runtime
	for n := range runtimes {
		if runtimes[n].ID == id {
			candidate = &runtimes[n]
			break
		}
	}
	if candidate == nil {
		return nil, ErrRuntimeNotFound
	}
	if candidate.Installed {
		return candidate, nil
	}
	if _, err := m.runUV(ctx, []string{"python", "install", candidate.Key, "--install-dir", m.pythonsDir, "--no-bin", "--no-registry", "--managed-python", "--no-config"}); err != nil {
		return nil, err
	}

	runtimes, err = m.ListRuntimes(ctx, true, "", "")
	if err != nil {
		return nil, err
	}
	for n := range runtimes {
		runtime := &runtimes[n]
		if runtime.ID == id && runtime.Installed && runtime.Path != nil {
			result, runErr := m.run(ctx, *runtime.Path, []string{"--version"}, m.environment())
			if runErr != nil || !strings.Contains(result.stdout+result.stderr, runtime.Version) {
				return nil, fmt.Errorf("installed Python runtime failed version validation")
			}
			return runtime, nil
		}
	}
	return nil, fmt.Errorf("%w after installation", ErrRuntimeNotInstalled)
}

func canonicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func pathDigest(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}

func pathWithin(path, root string) bool {
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(absoluteRoot, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator))
}
