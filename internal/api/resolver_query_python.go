package api

import (
	"context"
	"errors"

	"github.com/stashapp/stash/internal/manager"
	"github.com/stashapp/stash/pkg/python"
)

func pythonManager() (*manager.Manager, *python.Manager, error) {
	mgr := manager.GetInstance()
	if mgr.PythonManager == nil {
		return mgr, nil, errors.New("python manager is not initialized")
	}
	return mgr, mgr.PythonManager, nil
}

func (r *queryResolver) PythonStatus(ctx context.Context) (*python.Status, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return nil, err
	}
	configuredPath := mgr.Config.GetPythonPath()
	runtimeID := mgr.Config.GetPythonRuntimeID()
	status := &python.Status{
		Manager:              service.EngineStatus(ctx),
		ConfiguredPythonPath: configuredPath,
	}
	if !status.Manager.Installed {
		return status, nil
	}
	runtimes, err := service.ListRuntimes(ctx, false, runtimeID, configuredPath)
	if err != nil {
		return nil, err
	}
	for n := range runtimes {
		if runtimes[n].Selected {
			status.SelectedRuntime = &runtimes[n]
			break
		}
	}
	if runtimeID != "" && service.CheckEnvironment(ctx, runtimeID, configuredPath) == nil {
		status.ManagedEnvironment = true
	}
	return status, nil
}

func (r *queryResolver) PythonRuntimes(ctx context.Context, allVersions *bool) ([]*python.Runtime, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return nil, err
	}
	if !service.EngineStatus(ctx).Installed {
		return []*python.Runtime{}, nil
	}
	all := allVersions != nil && *allVersions
	runtimes, err := service.ListRuntimes(ctx, all, mgr.Config.GetPythonRuntimeID(), mgr.Config.GetPythonPath())
	if err != nil {
		return nil, err
	}
	ret := make([]*python.Runtime, len(runtimes))
	for n := range runtimes {
		ret[n] = &runtimes[n]
	}
	return ret, nil
}

func (r *queryResolver) PythonPackages(ctx context.Context, outdated *bool) ([]*python.Package, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return nil, err
	}
	resolved, err := python.ResolveSelection(mgr.Config.GetPythonRuntimeID(), mgr.Config.GetPythonPath())
	if err != nil {
		return nil, err
	}
	packages, err := service.ListPackages(ctx, string(*resolved), outdated != nil && *outdated, mgr.Config.GetPythonIndexes())
	if err != nil {
		return nil, err
	}
	ret := make([]*python.Package, len(packages))
	for n := range packages {
		ret[n] = &packages[n]
	}
	return ret, nil
}

func (r *queryResolver) PythonIndexes(ctx context.Context) ([]*python.Index, error) {
	mgr, _, err := pythonManager()
	if err != nil {
		return nil, err
	}
	indexes := mgr.Config.GetPythonIndexes()
	ret := make([]*python.Index, len(indexes))
	for n := range indexes {
		ret[n] = &indexes[n]
	}
	return ret, nil
}

func (r *queryResolver) PythonCatalogs(ctx context.Context) ([]*python.CatalogStatus, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return nil, err
	}
	statuses, err := service.CatalogStatuses(ctx, mgr.Config.GetPythonIndexes())
	if err != nil {
		return nil, err
	}
	ret := make([]*python.CatalogStatus, len(statuses))
	for n := range statuses {
		ret[n] = &statuses[n]
	}
	return ret, nil
}

func (r *queryResolver) SearchPythonPackages(ctx context.Context, query string, index *string, limit *int) ([]*python.PackageSearchResult, error) {
	mgr, service, err := pythonManager()
	if err != nil {
		return nil, err
	}
	requestedLimit := 50
	if limit != nil {
		requestedLimit = *limit
	}
	results, err := service.SearchPackages(ctx, query, index, requestedLimit, mgr.Config.GetPythonIndexes())
	if err != nil {
		return nil, err
	}
	ret := make([]*python.PackageSearchResult, len(results))
	for n := range results {
		ret[n] = &results[n]
	}
	return ret, nil
}
