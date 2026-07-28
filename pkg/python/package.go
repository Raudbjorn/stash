package python

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

var packageNamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
var normalizationPattern = regexp.MustCompile(`[-_.]+`)

func normalizeProjectName(name string) string {
	return normalizationPattern.ReplaceAllString(strings.ToLower(name), "-")
}

func ValidatePackageNames(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, errors.New("at least one Python package name is required")
	}
	if len(names) > 100 {
		return nil, errors.New("a Python package operation is limited to 100 names")
	}
	ret := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if !packageNamePattern.MatchString(name) {
			return nil, fmt.Errorf("invalid Python package name %q", raw)
		}
		normalized := normalizeProjectName(name)
		if _, duplicate := seen[normalized]; duplicate {
			return nil, fmt.Errorf("duplicate Python package name %q", name)
		}
		seen[normalized] = struct{}{}
		ret = append(ret, name)
	}
	return ret, nil
}

func (m *Manager) ListPackages(ctx context.Context, executable string, outdated bool, indexes []Index) ([]Package, error) {
	configPath, cleanup, err := m.WriteCommandConfig(indexes)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	installed, err := m.listPackages(ctx, executable, false, configPath)
	if err != nil || !outdated {
		return installed, err
	}
	outdatedPackages, err := m.listPackages(ctx, executable, true, configPath)
	if err != nil {
		return nil, err
	}
	latest := make(map[string]Package, len(outdatedPackages))
	for _, pkg := range outdatedPackages {
		latest[normalizeProjectName(pkg.Name)] = pkg
	}
	for n := range installed {
		if available, ok := latest[normalizeProjectName(installed[n].Name)]; ok {
			installed[n].LatestVersion = available.LatestVersion
			installed[n].LatestFiletype = available.LatestFiletype
		}
	}
	return installed, nil
}

func (m *Manager) listPackages(ctx context.Context, executable string, outdated bool, configPath string) ([]Package, error) {
	args := []string{"--config-file", configPath, "pip", "list", "--format", "json", "--python", executable, "--no-python-downloads"}
	if outdated {
		args = append(args, "--outdated")
	}
	result, err := m.runUV(ctx, args)
	if err != nil {
		return nil, err
	}
	var packages []Package
	if err := json.Unmarshal([]byte(result.stdout), &packages); err != nil {
		return nil, fmt.Errorf("decode installed Python packages: %w", err)
	}
	sort.SliceStable(packages, func(a, b int) bool { return strings.ToLower(packages[a].Name) < strings.ToLower(packages[b].Name) })
	return packages, nil
}

func (m *Manager) InstallPackages(ctx context.Context, runtimeID, executable string, names []string, indexes []Index, reporter Reporter) error {
	validated, err := ValidatePackageNames(names)
	if err != nil {
		return err
	}
	return m.mutatePackages(ctx, runtimeID, executable, indexes, reporter, "install", validated)
}

func (m *Manager) UninstallPackages(ctx context.Context, runtimeID, executable string, names []string, indexes []Index, reporter Reporter) error {
	validated, err := ValidatePackageNames(names)
	if err != nil {
		return err
	}
	return m.mutatePackages(ctx, runtimeID, executable, indexes, reporter, "uninstall", validated)
}

func (m *Manager) UpdatePackages(ctx context.Context, runtimeID, executable string, names *[]string, indexes []Index, reporter Reporter) error {
	var selected []string
	if names == nil {
		packages, err := m.ListPackages(ctx, executable, true, indexes)
		if err != nil {
			return err
		}
		for _, pkg := range packages {
			if pkg.LatestVersion != nil {
				selected = append(selected, pkg.Name)
			}
		}
		if len(selected) == 0 {
			return errors.New("all Python packages are already up to date")
		}
	} else {
		var err error
		selected, err = ValidatePackageNames(*names)
		if err != nil {
			return err
		}
	}
	return m.mutatePackages(ctx, runtimeID, executable, indexes, reporter, "update", selected)
}

func (m *Manager) mutatePackages(ctx context.Context, runtimeID, executable string, indexes []Index, reporter Reporter, operation string, names []string) error {
	m.mutationMu.Lock()
	defer m.mutationMu.Unlock()
	release, err := AcquireMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	if reporter != nil {
		reporter(Event{Phase: PhaseResolving, Message: "Resolving packages"})
	}
	if err := m.CheckEnvironment(ctx, runtimeID, executable); err != nil {
		return err
	}
	configPath, cleanupConfig, err := m.WriteCommandConfig(indexes)
	if err != nil {
		return err
	}
	defer cleanupConfig()

	freeze, err := m.runUV(ctx, []string{"--config-file", configPath, "pip", "freeze", "--python", executable, "--no-python-downloads"})
	if err != nil {
		return err
	}
	snapshot, err := os.CreateTemp(m.cacheDir, ".python-packages-*.txt")
	if err != nil {
		return err
	}
	snapshotPath := snapshot.Name()
	defer os.Remove(snapshotPath)
	if err := snapshot.Chmod(0600); err != nil {
		snapshot.Close()
		return err
	}
	if _, err := snapshot.WriteString(freeze.stdout); err != nil {
		snapshot.Close()
		return err
	}
	if err := snapshot.Close(); err != nil {
		return err
	}

	args := []string{"--config-file", configPath, "pip"}
	switch operation {
	case "install":
		args = append(args, "install", "--python", executable, "--no-python-downloads")
	case "update":
		args = append(args, "install", "--upgrade", "--python", executable, "--no-python-downloads")
	case "uninstall":
		args = append(args, "uninstall", "--python", executable, "--no-python-downloads")
	default:
		return fmt.Errorf("unknown Python package operation %q", operation)
	}
	args = append(args, names...)
	result, mutationErr := m.runUV(ctx, args)
	reportPackagePhases(result.stderr, reporter)
	if mutationErr == nil {
		if reporter != nil {
			reporter(Event{Phase: PhaseValidating, Message: "Validating environment"})
		}
		_, mutationErr = m.runUV(ctx, []string{"--config-file", configPath, "pip", "check", "--python", executable, "--no-python-downloads"})
	}
	if mutationErr != nil {
		rollbackContext, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		_, rollbackErr := m.runUV(rollbackContext, []string{"--config-file", configPath, "pip", "sync", snapshotPath, "--python", executable, "--no-python-downloads"})
		if rollbackErr != nil {
			return fmt.Errorf("%w; rollback failed: %v", mutationErr, rollbackErr)
		}
		return mutationErr
	}
	if reporter != nil {
		reporter(Event{Phase: PhaseComplete, Message: "Package environment is valid"})
	}
	return nil
}

func reportPackagePhases(stderr string, reporter Reporter) {
	if reporter == nil {
		return
	}
	for _, raw := range strings.Split(stderr, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "Resolved"):
			reporter(Event{Phase: PhasePreparing, Message: "Downloading and building"})
		case strings.HasPrefix(line, "Prepared"):
			reporter(Event{Phase: PhaseInstalling, Message: "Installing"})
		case strings.HasPrefix(line, "Installed"), strings.HasPrefix(line, "Uninstalled"):
			reporter(Event{Phase: PhaseValidating, Message: "Validating"})
		}
	}
}
