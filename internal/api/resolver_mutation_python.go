package api

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/stashapp/stash/internal/manager/task"
	"github.com/stashapp/stash/pkg/python"
)

func (r *mutationResolver) InstallPythonManager(ctx context.Context) (string, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return "", err
	}
	id := mgr.JobManager.Add(ctx, "Installing uv "+python.ManagedUVVersion+"...", &task.InstallPythonManagerJob{Manager: service})
	return strconv.Itoa(id), nil
}

func (r *mutationResolver) InstallPythonRuntime(ctx context.Context, id string) (string, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return "", err
	}
	jobID := mgr.JobManager.Add(ctx, "Installing Python runtime...", &task.InstallPythonRuntimeJob{Manager: service, RuntimeID: id})
	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) SelectPythonRuntime(ctx context.Context, id string) (string, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return "", err
	}
	jobID := mgr.JobManager.Add(ctx, "Selecting Python runtime...", &task.SelectPythonRuntimeJob{Manager: service, Config: mgr.Config, RuntimeID: id})
	return strconv.Itoa(jobID), nil
}

func (r *mutationResolver) InstallPythonPackages(ctx context.Context, names []string) (string, error) {
	return r.addPythonPackageJob(ctx, task.PythonPackageInstall, names, false)
}

func (r *mutationResolver) UpdatePythonPackages(ctx context.Context, names []string) (string, error) {
	return r.addPythonPackageJob(ctx, task.PythonPackageUpdate, names, names == nil)
}

func (r *mutationResolver) UninstallPythonPackages(ctx context.Context, names []string) (string, error) {
	return r.addPythonPackageJob(ctx, task.PythonPackageUninstall, names, false)
}

func (r *mutationResolver) addPythonPackageJob(ctx context.Context, operation task.PythonPackageOperation, names []string, all bool) (string, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return "", err
	}
	if mgr.Config.GetPythonRuntimeID() == "" {
		return "", fmt.Errorf("select a managed Python environment before changing packages")
	}
	var namePointer *[]string
	if !all {
		validated, err := python.ValidatePackageNames(names)
		if err != nil {
			return "", err
		}
		namePointer = &validated
	}
	description := "Changing Python packages"
	switch operation {
	case task.PythonPackageInstall:
		description = "Installing Python packages"
	case task.PythonPackageUpdate:
		description = "Updating Python packages"
	case task.PythonPackageUninstall:
		description = "Uninstalling Python packages"
	}
	if namePointer != nil {
		description += ": " + boundedPackageNames(*namePointer)
	}
	jobID := mgr.JobManager.Add(ctx, description+"...", &task.PythonPackagesJob{
		Manager: service, Config: mgr.Config, Operation: operation, Names: namePointer,
	})
	return strconv.Itoa(jobID), nil
}

func boundedPackageNames(names []string) string {
	shown := names
	if len(shown) > 3 {
		shown = shown[:3]
	}
	parts := make([]string, len(shown))
	for n, name := range shown {
		if len(name) > 48 {
			name = name[:48] + "…"
		}
		parts[n] = name
	}
	ret := strings.Join(parts, ", ")
	if len(names) > len(shown) {
		ret += fmt.Sprintf(" and %d more", len(names)-len(shown))
	}
	return ret
}

func (r *mutationResolver) ConfigurePythonIndexes(ctx context.Context, indexes []*python.Index) ([]*python.Index, error) {
	mgr, _, err := pythonManager()
	if err != nil {
		return nil, err
	}
	values := make([]python.Index, len(indexes))
	for n, index := range indexes {
		if index == nil {
			return nil, fmt.Errorf("python package index %d is null", n+1)
		}
		values[n] = *index
	}
	if err := mgr.Config.UpdatePythonIndexes(values); err != nil {
		return nil, err
	}
	saved := mgr.Config.GetPythonIndexes()
	ret := make([]*python.Index, len(saved))
	for n := range saved {
		ret[n] = &saved[n]
	}
	return ret, nil
}

func (r *mutationResolver) RefreshPythonPackageCatalog(ctx context.Context, index *string) (string, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return "", err
	}
	configured := mgr.Config.GetPythonIndexes()
	selected := configured
	if index != nil {
		selected = nil
		for _, candidate := range configured {
			if strings.EqualFold(candidate.Name, *index) {
				selected = append(selected, candidate)
				break
			}
		}
		if len(selected) == 0 {
			return "", fmt.Errorf("unknown Python package index %q", *index)
		}
	}
	jobID := mgr.JobManager.Add(ctx, "Refreshing Python package catalog...", &task.RefreshPythonCatalogJob{Manager: service, Indexes: selected})
	return strconv.Itoa(jobID), nil
}
